package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	candidateRefreshDispatcherInterval = 30 * time.Second
	inventoryRefreshMaxEntriesPerTask  = 50
)

func RegisterAllocationTaskHandlers(mux *asynq.ServeMux, module *Module) func(context.Context) {
	mux.HandleFunc(allocinfra.TypeCandidateRefreshDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		result, err := module.UseCase.DispatchCandidateRefreshes(ctx, 0)
		if err != nil {
			slog.Warn("candidate refresh dispatcher failed", "error", err)
			return err
		}
		if result != nil && result.Attempted > 0 {
			slog.Info(
				"candidate refresh dispatcher finished",
				"attempted", result.Attempted,
				"queued", result.Queued,
				"failed", result.Failed,
			)
		}
		return nil
	})
	mux.HandleFunc(allocinfra.TypeCandidateRefresh, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		var payload allocapp.CandidateRefreshTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode candidate refresh task: %w: %w", err, asynq.SkipRetry)
		}
		slog.Info("processing candidate refresh task", "project_id", payload.ProjectID, "generation", payload.Generation, "request_id", payload.RequestID)
		if err := module.UseCase.ProcessCandidateRefresh(ctx, payload); err != nil {
			slog.Warn("candidate refresh task failed", "project_id", payload.ProjectID, "generation", payload.Generation, "request_id", payload.RequestID, "error", err)
			if errors.Is(err, domain.ErrAllocationNotFound) || errors.Is(err, domain.ErrInvalidAllocationRequest) {
				return fmt.Errorf("non-retryable candidate refresh task failure: %w: %w", err, asynq.SkipRetry)
			}
			return err
		}
		slog.Info("candidate refresh task finished", "project_id", payload.ProjectID, "generation", payload.Generation, "request_id", payload.RequestID)
		return nil
	})

	mux.HandleFunc(allocinfra.TypeInventoryRefresh, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.UseCase == nil {
			return nil
		}
		release, admitted := acquireInventoryRefreshCapacity(module)
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
		return nil
	})
	if module == nil || module.UseCase == nil {
		return func(context.Context) {}
	}
	module.UseCase.ScheduleCandidateRefreshDispatcher(context.Background(), 0)
	return startAllocationTaskSeeders(module, candidateRefreshDispatcherInterval, allocapp.InventoryRefreshInterval)
}

func refreshInventoryTask(ctx context.Context, useCase *allocapp.UseCase) (*allocapp.InventoryRefreshResult, bool, error) {
	total := &allocapp.InventoryRefreshResult{}
	activeBefore := time.Now()
	for total.Attempted < inventoryRefreshMaxEntriesPerTask {
		batch, err := useCase.RefreshInventoryCacheBefore(ctx, activeBefore)
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

func startAllocationTaskSeeders(module *Module, candidateInterval time.Duration, inventoryInterval time.Duration) func(context.Context) {
	if module == nil || module.UseCase == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		candidateTicker := time.NewTicker(candidateInterval)
		inventoryTicker := time.NewTicker(inventoryInterval)
		defer candidateTicker.Stop()
		defer inventoryTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-candidateTicker.C:
				module.UseCase.ScheduleCandidateRefreshDispatcher(ctx, 0)
			case <-inventoryTicker.C:
				if err := module.UseCase.ScheduleInventoryRefresh(ctx); err != nil {
					slog.Warn("enqueue inventory cache refresh failed", "error", err)
				}
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

func acquireInventoryRefreshCapacity(module *Module) (func(), bool) {
	if module == nil || module.BackgroundExecution == nil {
		return func() {}, true
	}
	release, admitted := module.BackgroundExecution.TryAcquire()
	if release == nil {
		release = func() {}
	}
	return release, admitted
}
