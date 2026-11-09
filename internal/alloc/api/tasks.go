package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const inventoryRefreshMaxEntriesPerTask = 50
const inventoryOrderActivityRefreshInterval = 30 * time.Minute

func RegisterAllocationTaskHandlers(mux *asynq.ServeMux, module *Module) func(context.Context) {
	if module != nil && module.InventoryRefreshExecution == nil {
		module.InventoryRefreshExecution = newInventoryRefreshGate(allocapp.InventoryRefreshConcurrency)
	}
	mux.HandleFunc(allocinfra.TypePrivateInventoryRefresh, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		var payload allocinfra.PrivateInventoryRefreshPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil || payload.ProjectID == 0 || payload.ViewerUserID == 0 {
			return fmt.Errorf("invalid private inventory refresh payload: %w", asynq.SkipRetry)
		}
		release, admitted := acquireInventoryRefreshCapacity(ctx, module)
		if !admitted {
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		return module.UseCase.RefreshPrivateInventory(ctx, payload.ProjectID, payload.ViewerUserID)
	})
	mux.HandleFunc(allocinfra.TypeInventoryRefresh, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		release, admitted := acquireInventoryRefreshCapacity(ctx, module)
		if !admitted {
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		result, deferred, err := refreshInventoryTask(ctx, module.UseCase)
		if err != nil {
			slog.Warn("inventory cache refresh failed", "error", err)
			return err
		}
		if result != nil && result.Failed > 0 {
			slog.Warn(
				"inventory cache refresh finished with failures",
				"attempted", result.Attempted,
				"updated", result.Updated,
				"removed", result.Removed,
				"skipped", result.Skipped,
				"failed", result.Failed,
				"error", result.LastError,
			)
		} else if result != nil && result.Attempted > 0 {
			slog.Info("inventory cache refresh finished", "attempted", result.Attempted, "updated", result.Updated, "removed", result.Removed, "skipped", result.Skipped)
		}
		if deferred {
			return platform.ErrBackgroundExecutionDeferred
		}
		if result != nil && result.Attempted >= inventoryRefreshMaxEntriesPerTask {
			if err := module.UseCase.ScheduleInventoryRefreshContinuation(ctx); err != nil {
				return fmt.Errorf("enqueue inventory cache refresh continuation: %w", err)
			}
		}
		return nil
	})
	if module == nil || module.UseCase == nil {
		return func(context.Context) {}
	}
	return startInventoryRefreshSeeder(module, allocapp.InventoryRefreshIntervalValue)
}

func refreshInventoryTask(ctx context.Context, useCase *allocapp.UseCase) (*allocapp.InventoryRefreshResult, bool, error) {
	total := &allocapp.InventoryRefreshResult{}
	if err := useCase.EnsureInventoryRefreshSchedule(ctx); err != nil {
		return total, false, err
	}
	dueBefore := time.Now()
	for total.Attempted < inventoryRefreshMaxEntriesPerTask {
		batch, err := useCase.RefreshInventoryCacheBefore(ctx, dueBefore)
		if err != nil {
			return total, false, err
		}
		if batch == nil || batch.Attempted == 0 {
			return total, false, nil
		}
		total.Attempted += batch.Attempted
		total.Updated += batch.Updated
		total.Removed += batch.Removed
		total.Skipped += batch.Skipped
		total.Failed += batch.Failed
		if batch.LastError != nil {
			total.LastError = batch.LastError
		}
		if batch.Failed > 0 {
			if batch.LastError == nil {
				batch.LastError = errors.New("inventory refresh failed")
			}
			return total, false, batch.LastError
		}
		if batch.Skipped > 0 {
			return total, true, nil
		}
	}
	return total, false, nil
}

func startInventoryRefreshSeeder(module *Module, interval func() time.Duration) func(context.Context) {
	if module == nil || module.UseCase == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := module.UseCase.RefreshInventoryOrderActivity(ctx); err != nil {
			slog.Warn("sample recent inventory order activity failed", "error", err)
		}
		if err := module.UseCase.NormalizeInventoryRefreshSchedule(ctx); err != nil {
			slog.Warn("normalize inventory refresh schedule failed", "error", err)
		}
		if err := module.UseCase.WarmFastInventoryBase(ctx); err != nil {
			slog.Warn("warm fast inventory base failed", "error", err)
		}
		if err := module.UseCase.ScheduleInventoryRefresh(ctx); err != nil {
			slog.Warn("enqueue initial inventory cache refresh failed", "error", err)
		}
		pollInterval := func() time.Duration {
			return min(interval(), time.Minute)
		}
		timer := time.NewTimer(pollInterval())
		defer timer.Stop()
		activityTimer := time.NewTicker(inventoryOrderActivityRefreshInterval)
		defer activityTimer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-activityTimer.C:
				if err := module.UseCase.WarmFastInventoryBase(ctx); err != nil {
					slog.Warn("warm fast inventory base failed", "error", err)
				}
				if err := module.UseCase.RefreshInventoryOrderActivity(ctx); err != nil {
					slog.Warn("sample recent inventory order activity failed", "error", err)
				}
			case <-timer.C:
				if err := module.UseCase.WarmFastInventoryBase(ctx); err != nil {
					slog.Warn("warm fast inventory base failed", "error", err)
				}
				if err := module.UseCase.ScheduleInventoryRefresh(ctx); err != nil {
					slog.Warn("enqueue inventory cache refresh failed", "error", err)
				}
				timer.Reset(pollInterval())
			}
		}
	}()
	return func(shutdownCtx context.Context) {
		cancel()
		select {
		case <-done:
		case <-shutdownCtx.Done():
		}
	}
}

func acquireInventoryRefreshCapacity(ctx context.Context, module *Module) (func(), bool) {
	if module == nil {
		return func() {}, true
	}
	var releases []func()
	if module.InventoryRefreshExecution != nil {
		release, admitted := platform.AcquireBackgroundExecution(ctx, module.InventoryRefreshExecution)
		if !admitted {
			return func() {}, false
		}
		releases = append(releases, release)
	}
	if module.BackgroundExecution != nil {
		release, admitted := platform.AcquireBackgroundExecution(ctx, module.BackgroundExecution)
		if !admitted {
			for _, release := range releases {
				release()
			}
			return func() {}, false
		}
		releases = append(releases, release)
	}
	return func() {
		for _, release := range releases {
			release()
		}
	}, true
}
