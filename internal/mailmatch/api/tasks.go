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
	fetchDispatcherInterval     = 15 * time.Second
	projectHistoryConcurrency   = 4 // ponytail: fixed per-process ceiling; use a dedicated server if horizontal replicas need a cluster-wide cap.
	projectHistoryDispatchLimit = 4
	backgroundReleaseTimeout    = 5 * time.Second
)

var projectHistorySlots = make(chan struct{}, projectHistoryConcurrency)

func RegisterTaskHandlers(mux *asynq.ServeMux, module *Module) {
	mux.HandleFunc(mailmatchinfra.TypeMailmatchFetchDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil {
			return nil
		}
		var dispatchErrors []error
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

	pickupFetchHandler := func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		var payload mailmatchapp.FetchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode mailmatch fetch task: %w: %w", err, asynq.SkipRetry)
		}
		if err := module.UseCase.ProcessFetch(ctx, payload); err != nil {
			slog.Warn("mailmatch fetch task failed", "resource_id", payload.EmailResourceID, "order_no", payload.OrderNo, "error", err)
			return err
		}
		slog.Info("mailmatch fetch task finished", "resource_id", payload.EmailResourceID, "order_no", payload.OrderNo)
		return nil
	}
	// The legacy type is retained only so tasks queued by the previous release
	// can be acknowledged without touching persistent resource fetch state.
	mux.HandleFunc(mailmatchinfra.TypeMailmatchFetch, pickupFetchHandler)
	mux.HandleFunc(mailmatchinfra.TypeMailmatchPickupFetch, pickupFetchHandler)
	mux.HandleFunc(mailmatchinfra.TypeMailmatchPickupRequestFetch, func(ctx context.Context, task *asynq.Task) error {
		return processPickupRequestFetchTask(ctx, task, module)
	})

	mux.HandleFunc(mailmatchinfra.TypeMailmatchResourceFetch, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ResourceFetch == nil {
			return nil
		}
		var payload mailmatchapp.ResourceFetchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode mailmatch resource fetch task: %w: %w", err, asynq.SkipRetry)
		}
		release, admitted := acquireBackgroundExecution(module)
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundReleaseTimeout)
				defer cancel()
				return module.ResourceFetch.ReleaseDispatch(recoveryCtx, payload)
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.ResourceFetch.Process(ctx, payload); err != nil {
			slog.Warn(
				"mailmatch resource fetch task failed",
				"resource_id", payload.ResourceID,
				"generation", payload.Generation,
				"request_id", payload.RequestID,
				"error", err,
			)
			return err
		}
		slog.Info(
			"mailmatch resource fetch task finished",
			"resource_id", payload.ResourceID,
			"generation", payload.Generation,
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
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundReleaseTimeout)
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
				"project_id", payload.ProjectID,
				"generation", payload.Generation,
				"error", err,
			)
			return err
		}
		return nil
	})

	mux.HandleFunc(mailmatchinfra.TypeValidatedMicrosoftHistoryScan, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ProjectHistory == nil {
			return nil
		}
		var payload mailmatchapp.ValidatedMicrosoftHistoryScanTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode validated microsoft history scan task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.ResourceID == 0 {
			return fmt.Errorf("decode validated microsoft history scan task: resource identity is missing: %w", asynq.SkipRetry)
		}
		release, admitted := acquireProjectHistoryCapacity(module)
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return nil
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.ProjectHistory.ProcessValidatedMicrosoftHistory(ctx, payload); err != nil {
			if !errors.Is(err, platform.ErrBackgroundExecutionDeferred) {
				slog.Warn(
					"validated microsoft history scan task failed",
					"resource_id", payload.ResourceID,
					"request_id", payload.RequestID,
					"error", err,
				)
			}
			return err
		}
		return nil
	})

	mux.HandleFunc(mailmatchinfra.TypeProjectHistoryDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.ProjectHistory == nil {
			return nil
		}
		if err := module.ProjectHistory.DispatchPending(ctx, projectHistoryDispatchLimit); err != nil {
			slog.Warn("project history dispatcher failed", "error", err)
		}
		return nil
	})
}

func processPickupRequestFetchTask(ctx context.Context, task *asynq.Task, module *Module) error {
	if module == nil || module.UseCase == nil {
		return nil
	}
	var payload mailmatchapp.PickupRequestFetchTask
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		platform.RecordTaskEvent("pickup_request_fetch", "failed")
		slog.Warn("discarding invalid pickup request fetch task", "error", err)
		return nil
	}
	startedAt := time.Now()
	size := pickupRequestTaskSize(len(payload.Scopes))
	platform.RecordTaskEvent("pickup_request_fetch", "started")
	platform.ObserveQueueWait("pickup_request_fetch", payload.RequestedAt)
	if !payload.RequestedAt.IsZero() && !startedAt.Before(payload.RequestedAt.Add(2*time.Minute)) {
		_ = module.UseCase.ProcessPickupRequestFetch(ctx, payload)
		platform.RecordTaskEvent("pickup_request_fetch", "expired")
		platform.ObserveServiceDuration("pickup_fetch", size, "expired", startedAt)
		platform.ObserveServiceEndToEnd("pickup_fetch", size, "expired", payload.RequestedAt)
		return nil
	}
	outcome, err := module.UseCase.ProcessPickupRequestFetchWithOutcome(ctx, payload)
	result := pickupRequestTaskResult(outcome, err)
	platform.RecordTaskEvent("pickup_request_fetch", result)
	platform.ObserveServiceDuration("pickup_fetch", size, result, startedAt)
	platform.ObserveServiceEndToEnd("pickup_fetch", size, result, payload.RequestedAt)
	if err != nil {
		slog.Warn("pickup request fetch task completed with scope failures", "scopes", len(payload.Scopes), "error", err)
		return nil
	}
	return nil
}

func pickupRequestTaskResult(outcome mailmatchapp.PickupRequestFetchOutcome, err error) string {
	switch {
	case err != nil && outcome.Succeeded+outcome.NoWork > 0:
		return "partial"
	case err != nil:
		return "system_failed"
	case outcome.Expired > 0 && outcome.Succeeded+outcome.NoWork > 0:
		return "partial"
	case outcome.Expired > 0:
		return "expired"
	case outcome.Succeeded > 0:
		return "succeeded"
	default:
		return "no_work"
	}
}

func pickupRequestTaskSize(quantity int) string {
	switch {
	case quantity <= 1:
		return "single"
	case quantity <= 20:
		return "002_020"
	case quantity <= 50:
		return "021_050"
	case quantity <= 100:
		return "051_100"
	default:
		return "101_200"
	}
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
	backgroundRelease, admitted := acquireBackgroundExecution(module)
	if !admitted {
		return func() {}, false
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

func acquireBackgroundExecution(module *Module) (func(), bool) {
	if module == nil || module.BackgroundExecution == nil {
		return func() {}, true
	}
	release, admitted := module.BackgroundExecution.TryAcquire()
	if release == nil {
		release = func() {}
	}
	return release, admitted
}

func scheduleMailmatchFetchDispatcher(ctx context.Context, module *Module, delay time.Duration) {
	if module == nil || module.ResourceFetch == nil {
		return
	}
	module.ResourceFetch.ScheduleDispatcher(ctx, delay)
}
