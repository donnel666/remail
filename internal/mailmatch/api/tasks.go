package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const (
	fetchDispatcherInterval        = 15 * time.Second
	projectHistoryConcurrency      = 4
	projectHistoryDispatchLimit    = 4
	backgroundReleaseTimeout       = 5 * time.Second
	maxFetchDispatcherInterval     = time.Hour
	maxProjectHistoryConcurrency   = 8096
	maxProjectHistoryDispatchLimit = 100
)

var projectHistoryActive atomic.Int64

func RegisterTaskHandlers(mux *asynq.ServeMux, module *Module) {
	// These two dispatchers are intentionally separate. Administrator mail
	// fetches are foreground work; project-history identification remains on
	// the paused/background queue and never competes for the same dispatcher.
	mux.HandleFunc(mailmatchinfra.TypeMailmatchAdminResourceFetchDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.AdminResourceFetch == nil {
			return nil
		}
		result, err := module.AdminResourceFetch.DispatchPending(ctx, resourceFetchDispatchLimitValue())
		if err != nil {
			slog.Warn("admin resource fetch dispatcher failed", "error", err)
			return err
		}
		if result != nil && result.Attempted > 0 {
			slog.Info("admin resource fetch dispatcher finished", "attempted", result.Attempted, "queued", result.Queued, "failed", result.Failed)
		}
		return nil
	})
	mux.HandleFunc(mailmatchinfra.TypeMailmatchResourceHistoryDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.ResourceHistory == nil {
			return nil
		}
		result, err := module.ResourceHistory.DispatchPending(ctx, resourceFetchDispatchLimitValue())
		if err != nil {
			slog.Warn("resource history dispatcher failed", "error", err)
			return err
		}
		if result != nil && result.Attempted > 0 {
			slog.Info("resource history dispatcher finished", "attempted", result.Attempted, "queued", result.Queued, "failed", result.Failed)
		}
		return nil
	})
	if module != nil && (module.UseCase != nil || module.AdminResourceFetch != nil || module.ResourceHistory != nil || module.ProjectHistory != nil) {
		if module.AdminResourceFetch != nil {
			module.AdminResourceFetch.ScheduleDispatcher(context.Background(), 0)
		}
		if module.ResourceHistory != nil {
			module.ResourceHistory.ScheduleDispatcher(context.Background(), 0)
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
	mux.HandleFunc(mailmatchinfra.TypeMailmatchFetch, pickupFetchHandler)
	mux.HandleFunc(mailmatchinfra.TypeMailmatchPickupFetch, pickupFetchHandler)
	mux.HandleFunc(mailmatchinfra.TypeMailmatchPickupRequestFetch, func(ctx context.Context, task *asynq.Task) error {
		return processPickupRequestFetchTask(ctx, task, module)
	})

	mux.HandleFunc(mailmatchinfra.TypeMailmatchAdminResourceFetch, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.AdminResourceFetch == nil {
			return nil
		}
		var payload mailmatchapp.AdminResourceFetchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode admin resource fetch task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.ResourceID == 0 || payload.Generation == 0 {
			return fmt.Errorf("decode admin resource fetch task: invalid payload: %w", asynq.SkipRetry)
		}
		if err := module.AdminResourceFetch.Process(ctx, payload); err != nil {
			slog.Warn("admin resource fetch task failed", "resource_id", payload.ResourceID, "generation", payload.Generation, "request_id", payload.RequestID, "error", err)
			return err
		}
		slog.Info("admin resource fetch task finished", "resource_id", payload.ResourceID, "generation", payload.Generation, "request_id", payload.RequestID)
		return nil
	})

	mux.HandleFunc(mailmatchinfra.TypeMailmatchResourceHistory, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ResourceHistory == nil {
			return nil
		}
		var payload mailmatchapp.ResourceHistoryTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode resource history task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.ResourceID == 0 || payload.Generation == 0 {
			return fmt.Errorf("decode resource history task: invalid payload: %w", asynq.SkipRetry)
		}
		release, admitted := acquireBackgroundExecution(ctx, module)
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundReleaseTimeout)
				defer cancel()
				return module.ResourceHistory.ReleaseDispatch(recoveryCtx, payload)
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.ResourceHistory.Process(ctx, payload); err != nil {
			slog.Warn("resource history task failed", "resource_id", payload.ResourceID, "generation", payload.Generation, "request_id", payload.RequestID, "error", err)
			return err
		}
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
		release, admitted := acquireProjectHistoryCapacity(ctx, module)
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
		release, admitted := acquireProjectHistoryCapacity(ctx, module)
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
			if retried, ok := asynq.GetRetryCount(ctx); ok {
				return capValidatedMicrosoftHistoryRetry(retried, err)
			}
			return err
		}
		return nil
	})

	mux.HandleFunc(mailmatchinfra.TypeProjectHistoryDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.ProjectHistory == nil {
			return nil
		}
		limit := min(runtimeconfig.Int("project_history_dispatch_limit", projectHistoryDispatchLimit, 1), maxProjectHistoryDispatchLimit)
		if err := module.ProjectHistory.DispatchPending(ctx, limit); err != nil {
			slog.Warn("project history dispatcher failed", "error", err)
		}
		return nil
	})
}

func capValidatedMicrosoftHistoryRetry(retried int, err error) error {
	if err == nil || retried < mailmatchinfra.ValidatedHistoryTaskMaxRetry || errors.Is(err, platform.ErrBackgroundExecutionDeferred) {
		return err
	}
	return fmt.Errorf("validated microsoft history scan retry limit reached: %w: %w", err, asynq.SkipRetry)
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
	if expiresAt := payload.EffectiveExpiresAt(); !expiresAt.IsZero() && !startedAt.Before(expiresAt) {
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
		if errors.Is(err, mailmatchapp.ErrPermanentMicrosoftFetchFailureHandling) {
			return err
		}
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

func resourceFetchDispatchLimitValue() int {
	return min(runtimeconfig.Int("resource_fetch_dispatch_limit", mailmatchapp.ResourceFetchDefaultDispatchLimit, 1), mailmatchapp.ResourceFetchDefaultDispatchLimit)
}

func startFetchDispatcherSeeder(module *Module) {
	if module == nil || (module.UseCase == nil && module.AdminResourceFetch == nil && module.ResourceHistory == nil && module.ProjectHistory == nil) {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastDispatch := time.Now()
		for now := range ticker.C {
			interval := min(runtimeconfig.Duration("fetch_dispatcher_interval_seconds", fetchDispatcherInterval, time.Second, 1), maxFetchDispatcherInterval)
			if now.Sub(lastDispatch) < interval {
				continue
			}
			lastDispatch = now
			if module.AdminResourceFetch != nil {
				module.AdminResourceFetch.ScheduleDispatcher(context.Background(), 0)
			}
			if module.ResourceHistory != nil {
				module.ResourceHistory.ScheduleDispatcher(context.Background(), 0)
			}
			if module.ProjectHistory != nil {
				module.ProjectHistory.ScheduleDispatcher(context.Background(), 0)
			}
		}
	}()
}

func acquireProjectHistoryCapacity(ctx context.Context, module *Module) (func(), bool) {
	backgroundRelease, admitted := acquireBackgroundExecution(ctx, module)
	if !admitted {
		return func() {}, false
	}
	limit := int64(min(runtimeconfig.Int("project_history_concurrency", projectHistoryConcurrency, 1), maxProjectHistoryConcurrency))
	for {
		active := projectHistoryActive.Load()
		if active >= limit {
			backgroundRelease()
			return func() {}, false
		}
		if projectHistoryActive.CompareAndSwap(active, active+1) {
			return func() {
				projectHistoryActive.Add(-1)
				backgroundRelease()
			}, true
		}
	}
}

func acquireBackgroundExecution(ctx context.Context, module *Module) (func(), bool) {
	if module == nil || module.BackgroundExecution == nil {
		return func() {}, true
	}
	return platform.AcquireBackgroundExecution(ctx, module.BackgroundExecution)
}
