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
	"github.com/hibiken/asynq"
)

const fetchDispatcherInterval = 15 * time.Second

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
	if module != nil && (module.UseCase != nil || module.ResourceFetch != nil) {
		scheduleMailmatchFetchDispatcher(context.Background(), module, 0)
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
}

func startFetchDispatcherSeeder(module *Module) {
	if module == nil || (module.UseCase == nil && module.ResourceFetch == nil) {
		return
	}
	go func() {
		ticker := time.NewTicker(fetchDispatcherInterval)
		defer ticker.Stop()
		for range ticker.C {
			scheduleMailmatchFetchDispatcher(context.Background(), module, 0)
		}
	}()
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
