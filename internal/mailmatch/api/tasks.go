package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	fetchDispatcherInterval      = 15 * time.Second
	projectHistoryConcurrency    = 4 // ponytail: fixed per-process ceiling; use a dedicated server if horizontal replicas need a cluster-wide cap.
	projectHistoryDispatchLimit  = 4
	projectHistoryReleaseTimeout = 5 * time.Second
)

var projectHistorySlots = make(chan struct{}, projectHistoryConcurrency)

func RegisterTaskHandlers(mux *asynq.ServeMux, module *Module) {
	mux.HandleFunc(mailmatchinfra.TypeMailmatchFetchDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil {
			return nil
		}
		defer scheduleMailmatchFetchDispatcher(context.Background(), module, fetchDispatcherInterval)
		var dispatchErrors []error
		if module.UseCase != nil {
			queued, err := module.UseCase.DispatchFetchJobs(ctx, 0)
			if err != nil {
				slog.Warn("mailmatch fetch dispatcher failed", "error", err)
				dispatchErrors = append(dispatchErrors, err)
			} else if queued > 0 {
				slog.Info("mailmatch fetch dispatcher finished", "queued", queued)
			}
		}
		if module.ResourceFetch != nil {
			result, err := module.ResourceFetch.DispatchPending(ctx, 0)
			if err != nil {
				slog.Warn("mailmatch resource fetch dispatcher failed", "error", err)
				dispatchErrors = append(dispatchErrors, err)
			} else if result != nil && result.Attempted > 0 {
				slog.Info(
					"mailmatch resource fetch dispatcher finished",
					"attempted", result.Attempted,
					"queued", result.Queued,
					"failed", result.Failed,
				)
			}
		}
		return errors.Join(dispatchErrors...)
	})
	if module != nil && (module.UseCase != nil || module.ResourceFetch != nil || module.ProjectHistory != nil) {
		if module.UseCase != nil || module.ResourceFetch != nil {
			scheduleMailmatchFetchDispatcher(context.Background(), module, 0)
		}
		if module.ProjectHistory != nil {
			module.ProjectHistory.ScheduleDispatcher(context.Background(), 0)
		}
		startFetchDispatcherSeeder(module)
	}

	mux.HandleFunc(mailmatchinfra.TypeMailmatchFetch, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		var payload mailmatchapp.FetchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode mailmatch fetch task: %w: %w", err, asynq.SkipRetry)
		}
		if err := module.UseCase.ProcessFetch(ctx, payload); err != nil {
			slog.Warn("mailmatch fetch task failed", "job_id", payload.JobID, "error", err)
			return err
		}
		slog.Info("mailmatch fetch task finished", "job_id", payload.JobID)
		return nil
	})

	mux.HandleFunc(mailmatchinfra.TypeMailmatchResourceFetch, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ResourceFetch == nil {
			return nil
		}
		var payload mailmatchapp.ResourceFetchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode mailmatch resource fetch task: %w: %w", err, asynq.SkipRetry)
		}
		if err := module.ResourceFetch.Process(ctx, payload); err != nil {
			slog.Warn(
				"mailmatch resource fetch task failed",
				"job_id", payload.JobID,
				"request_id", payload.RequestID,
				"error", err,
			)
			return err
		}
		slog.Info(
			"mailmatch resource fetch task finished",
			"job_id", payload.JobID,
			"request_id", payload.RequestID,
		)
		return nil
	})

	mux.HandleFunc(mailmatchinfra.TypeProjectHistoryScan, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ProjectHistory == nil {
			return nil
		}
		var payload mailmatchapp.ProjectHistoryScanTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode project history scan task: %w: %w", err, asynq.SkipRetry)
		}
		release, admitted := acquireProjectHistoryCapacity(module)
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectHistoryReleaseTimeout)
				defer cancel()
				if err := module.ProjectHistory.ReleaseDispatch(recoveryCtx, payload); err != nil {
					return err
				}
				return nil
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.ProjectHistory.Process(ctx, payload); err != nil {
			slog.Warn(
				"project history scan task failed",
				"job_id", payload.JobID,
				"error", err,
			)
			return err
		}
		return nil
	})

	mux.HandleFunc(mailmatchinfra.TypeProjectHistoryDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.ProjectHistory == nil {
			return nil
		}
		defer module.ProjectHistory.ScheduleDispatcher(context.Background(), fetchDispatcherInterval)
		if err := module.ProjectHistory.DispatchPending(ctx, projectHistoryDispatchLimit); err != nil {
			slog.Warn("project history dispatcher failed", "error", err)
		}
		return nil
	})
}

func startFetchDispatcherSeeder(module *Module) {
	if module == nil || (module.UseCase == nil && module.ResourceFetch == nil && module.ProjectHistory == nil) {
		return
	}
	go func() {
		ticker := time.NewTicker(fetchDispatcherInterval)
		defer ticker.Stop()
		for range ticker.C {
			scheduleMailmatchFetchDispatcher(context.Background(), module, 0)
			if module.ProjectHistory != nil {
				module.ProjectHistory.ScheduleDispatcher(context.Background(), 0)
			}
		}
	}()
}

func acquireProjectHistoryCapacity(module *Module) (func(), bool) {
	backgroundRelease := func() {}
	if module != nil && module.BackgroundExecution != nil {
		var admitted bool
		backgroundRelease, admitted = module.BackgroundExecution.TryAcquire()
		if !admitted {
			return func() {}, false
		}
		if backgroundRelease == nil {
			backgroundRelease = func() {}
		}
	}
	select {
	case projectHistorySlots <- struct{}{}:
		return func() {
			<-projectHistorySlots
			backgroundRelease()
		}, true
	default:
		backgroundRelease()
		return func() {}, false
	}
}

func scheduleMailmatchFetchDispatcher(ctx context.Context, module *Module, delay time.Duration) {
	if module == nil {
		return
	}
	if module.ResourceFetch != nil {
		module.ResourceFetch.ScheduleDispatcher(ctx, delay)
		return
	}
	if module.UseCase != nil {
		module.UseCase.ScheduleFetchDispatcher(ctx, delay)
	}
}
