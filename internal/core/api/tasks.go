package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	resourceValidationDispatcherInterval = 30 * time.Second
	resourceValidationDispatchMaximum    = 128
	backgroundLegacyReleaseTimeout       = 5 * time.Second
)

// StartCoreWorkers registers Core task handlers and starts the Asynq server.
func StartCoreWorkers(server *asynq.Server, module *CoreModule) error {
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)
	return server.Start(mux)
}

func RegisterCoreTaskHandlers(mux *asynq.ServeMux, module *CoreModule) {
	mux.HandleFunc(coreinfra.TypeAdminResourceBulkDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.AdminBulk == nil {
			return nil
		}
		// This dispatcher has no periodic seeder and performs only a bounded
		// DB-to-Redis handoff. Always let it seed at least one durable command;
		// the command handler itself remains governed by the global window.
		limit := max(1, backgroundDispatchLimit(module.BackgroundExecution, 32))
		if err := module.AdminBulk.DispatchPending(ctx, limit); err != nil {
			slog.Warn("admin resource bulk dispatcher failed", "error", err)
		}
		return nil
	})
	mux.HandleFunc(coreinfra.TypeAdminResourceBulk, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.AdminBulk == nil {
			return nil
		}
		var payload coreapp.AdminResourceBulkTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode admin resource bulk task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.CommandID == 0 || payload.DispatchToken == "" {
			return fmt.Errorf("decode admin resource bulk task: identity is missing: %w", asynq.SkipRetry)
		}
		release, admitted, err := tryAcquireBackgroundExecution(ctx, module.BackgroundExecution)
		if err != nil {
			return err
		}
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundLegacyReleaseTimeout)
				defer cancel()
				if err := module.AdminBulk.ReleaseDispatch(recoveryCtx, payload); err != nil {
					return fmt.Errorf("release legacy admin resource bulk dispatch: %w", err)
				}
				return nil
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.AdminBulk.Process(ctx, payload); err != nil {
			slog.Warn("admin resource bulk task failed", "command_id", payload.CommandID, "error", err)
			return err
		}
		return nil
	})
	mux.HandleFunc(coreinfra.TypeResourceValidationDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.ValidationUseCase == nil {
			return nil
		}
		limit := backgroundValidationAssignmentLimit(module.BackgroundExecution, resourceValidationDispatchMaximum)
		if limit <= 0 {
			return nil
		}
		result, err := module.ValidationUseCase.DispatchPending(ctx, limit)
		if err != nil {
			slog.Warn("resource validation dispatcher failed", "error", err)
			return nil
		}
		if result != nil && result.Attempted > 0 {
			slog.Info(
				"resource validation dispatcher finished",
				"attempted", result.Attempted,
				"queued", result.Queued,
				"failed", result.Failed,
			)
		}
		return nil
	})
	mux.HandleFunc(coreinfra.TypeResourceValidationBatch, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ValidationUseCase == nil {
			return nil
		}
		var payload coreapp.ResourceValidationBatchTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			slog.Warn("discard malformed resource validation batch task", "error", err)
			return nil
		}
		if payload.BatchID == "" || payload.OwnerUserID == 0 {
			slog.Warn("discard resource validation batch task without identity")
			cleanupResourceValidationBatch(ctx, module, payload)
			return nil
		}
		release, admitted, err := tryAcquireBackgroundExecution(ctx, module.BackgroundExecution)
		if err != nil {
			return err
		}
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				cleanupResourceValidationBatch(ctx, module, payload)
				return nil
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.ValidationUseCase.ProcessBatch(ctx, payload); err != nil {
			slog.Warn("resource validation batch task failed", "batch_id", payload.BatchID, "error", err)
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				cleanupResourceValidationBatch(ctx, module, payload)
				return nil
			}
			return err
		}
		return nil
	})
	mux.HandleFunc(coreinfra.TypeResourceValidation, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ValidationUseCase == nil {
			return nil
		}
		var payload coreapp.ResourceValidationTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			slog.Warn("discard malformed resource validation task", "error", err)
			return nil
		}
		if payload.ResourceID == 0 || payload.OwnerUserID == 0 || !coredomain.IsValidResourceType(payload.ResourceType) {
			slog.Warn("discard resource validation task without identity")
			return nil
		}
		release, admitted, err := tryAcquireBackgroundExecution(ctx, module.BackgroundExecution)
		if err != nil {
			return err
		}
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundLegacyReleaseTimeout)
				defer cancel()
				if err := module.ValidationUseCase.ReleaseDispatch(recoveryCtx, payload); err != nil {
					slog.Warn(
						"release exhausted resource validation admission failed",
						"resource_id", payload.ResourceID,
						"request_id", payload.RequestID,
						"error", err,
					)
				}
				module.ValidationUseCase.ScheduleDispatcher(recoveryCtx, time.Second)
				return nil
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		slog.Info(
			"processing resource validation task",
			"resource_id", payload.ResourceID,
			"request_id", payload.RequestID,
		)
		if err := module.ValidationUseCase.Process(ctx, payload); err != nil {
			slog.Warn(
				"resource validation task failed",
				"resource_id", payload.ResourceID,
				"request_id", payload.RequestID,
				"error", err,
			)
			return err
		}
		module.ValidationUseCase.ScheduleDispatcher(ctx, time.Second)
		slog.Info(
			"resource validation task finished",
			"resource_id", payload.ResourceID,
			"request_id", payload.RequestID,
		)
		return nil
	})

	mux.HandleFunc(coreinfra.TypeMicrosoftImport, func(ctx context.Context, task *asynq.Task) error {
		var payload coreapp.MicrosoftImportTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode microsoft import task: %w: %w", err, asynq.SkipRetry)
		}

		slog.Info(
			"processing microsoft import task",
			"import_id", payload.ImportID,
			"owner_user_id", payload.OwnerUserID,
			"request_id", payload.RequestID,
		)
		result, err := module.ImportUseCase.ProcessMicrosoftImport(ctx, payload)
		if err != nil {
			finalAttempt := isFinalAttempt(ctx)
			slog.Warn(
				"microsoft import task failed",
				"import_id", payload.ImportID,
				"owner_user_id", payload.OwnerUserID,
				"request_id", payload.RequestID,
				"final_attempt", finalAttempt,
				"error", err,
			)
			if payload.DispatchToken != "" {
				// Durable imports persist retry/terminal state under a fenced
				// claim. Returning the error to Asynq would replay a consumed token;
				// the periodic durable dispatcher issues a fresh token instead.
				return nil
			}
			if isNonRetryableImportError(err) {
				_ = module.ImportUseCase.MarkImportFailed(ctx, payload.ImportID, "Invalid import task.")
				return fmt.Errorf("non-retryable microsoft import task failure: %w: %w", err, asynq.SkipRetry)
			}
			if finalAttempt {
				_ = module.ImportUseCase.MarkImportFailed(ctx, payload.ImportID, "Import processing failed. Please retry later.")
			}
			return err
		}
		pendingValidations, err := queueMicrosoftImportValidations(ctx, module, result)
		if err != nil {
			slog.Warn(
				"microsoft import validation queue failed",
				"import_id", payload.ImportID,
				"owner_user_id", payload.OwnerUserID,
				"request_id", payload.RequestID,
				"error", err,
			)
		}
		slog.Info(
			"microsoft import task finished",
			"import_id", payload.ImportID,
			"owner_user_id", payload.OwnerUserID,
			"request_id", payload.RequestID,
			"validations_pending", pendingValidations,
		)
		return nil
	})
}

func cleanupResourceValidationBatch(ctx context.Context, module *CoreModule, task coreapp.ResourceValidationBatchTask) {
	if module == nil || module.ValidationUseCase == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundLegacyReleaseTimeout)
	defer cancel()
	if err := module.ValidationUseCase.ReleaseBatch(cleanupCtx, task); err != nil {
		slog.Warn("release resource validation batch lease failed", "batch_id", task.BatchID, "error", err)
	}
}

func tryAcquireBackgroundExecution(ctx context.Context, gate BackgroundExecutionGate) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return func() {}, false, err
	}
	if gate == nil {
		return func() {}, true, nil
	}
	release, admitted := gate.TryAcquire()
	if release == nil {
		release = func() {}
	}
	return release, admitted, nil
}

func backgroundDispatchLimit(gate BackgroundExecutionGate, maximum int) int {
	if maximum <= 0 {
		return 0
	}
	if gate == nil {
		return maximum
	}
	capacity, ok := gate.(interface{ Available() int })
	if !ok {
		return maximum
	}
	return min(maximum, max(0, capacity.Available()))
}

func backgroundValidationAssignmentLimit(gate BackgroundExecutionGate, maximum int) int {
	if maximum <= 0 {
		return 0
	}
	if gate == nil {
		return maximum
	}
	snapshotter, ok := gate.(interface {
		Snapshot() platform.BackgroundLoadSnapshot
	})
	if !ok {
		return backgroundDispatchLimit(gate, maximum)
	}
	return min(maximum, max(0, snapshotter.Snapshot().Limit))
}

func queueMicrosoftImportValidations(ctx context.Context, module *CoreModule, result *coreapp.MicrosoftImportProcessResult) (int, error) {
	if module == nil || module.ValidationUseCase == nil || result == nil || len(result.ImportedResourceIDs) == 0 {
		return 0, nil
	}
	module.ValidationUseCase.ScheduleDispatcher(ctx, 0)
	if module.AdminBulk != nil {
		module.AdminBulk.ScheduleDispatcher(ctx, 0)
	}
	if module.ImportUseCase != nil {
		if _, err := module.ImportUseCase.DispatchAdminImports(ctx, 100); err != nil {
			slog.Warn("resource import dispatcher failed", "error", err)
		}
	}
	return len(result.ImportedResourceIDs), nil
}

// StartResourceValidationDispatcher periodically recovers pending validation
// work until the returned cleanup function is called.
func StartResourceValidationDispatcher(ctx context.Context, module *CoreModule) func(context.Context) {
	return startResourceValidationDispatcher(ctx, module, resourceValidationDispatcherInterval)
}

func startResourceValidationDispatcher(ctx context.Context, module *CoreModule, interval time.Duration) func(context.Context) {
	if module == nil || module.ValidationUseCase == nil {
		return func(context.Context) {}
	}
	if interval <= 0 {
		interval = resourceValidationDispatcherInterval
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	if err := module.ValidationUseCase.ResetAssignments(ctx); err != nil {
		slog.Warn("reset resource validation assignments at startup failed", "error", err)
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				module.ValidationUseCase.ScheduleDispatcher(ctx, 0)
				if module.AdminBulk != nil {
					module.AdminBulk.ScheduleDispatcher(ctx, 0)
				}
				if module.ImportUseCase != nil {
					if _, err := module.ImportUseCase.DispatchAdminImports(ctx, 100); err != nil {
						slog.Warn("resource import dispatcher failed", "error", err)
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	var once sync.Once
	return func(shutdownCtx context.Context) {
		once.Do(cancel)
		select {
		case <-done:
		case <-shutdownCtx.Done():
		}
	}
}

func isFinalAttempt(ctx context.Context) bool {
	retried, retryOK := asynq.GetRetryCount(ctx)
	maxRetry, maxRetryOK := asynq.GetMaxRetry(ctx)
	return retryOK && maxRetryOK && retried >= maxRetry
}

func isNonRetryableImportError(err error) bool {
	return errors.Is(err, coredomain.ErrInvalidImportFormat) ||
		errors.Is(err, coredomain.ErrResourceNotFound) ||
		errors.Is(err, coredomain.ErrForbiddenResource) ||
		errors.Is(err, coredomain.ErrInvalidResourceStatus)
}
