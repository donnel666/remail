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
	"github.com/hibiken/asynq"
)

const (
	resourceValidationDispatcherInterval = 15 * time.Second
	resourceValidationDispatchMinimum    = 8
	resourceValidationDispatchMaximum    = 64
)

// StartCoreWorkers registers Core task handlers and starts the Asynq server.
func StartCoreWorkers(server *asynq.Server, module *CoreModule) error {
	mux := asynq.NewServeMux()
	RegisterCoreTaskHandlers(mux, module)
	return server.Start(mux)
}

func RegisterCoreTaskHandlers(mux *asynq.ServeMux, module *CoreModule) {
	mux.HandleFunc(coreinfra.TypeResourceValidationDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.ValidationUseCase == nil {
			return nil
		}
		limit := resourceValidationDispatchMaximum
		releaseBudget := func() {}
		if module.BackgroundDispatch != nil {
			limit, releaseBudget = module.BackgroundDispatch.AcquireDispatchBudget(
				ctx,
				coreinfra.ResourceValidationQueueName,
				resourceValidationDispatchMinimum,
				resourceValidationDispatchMaximum,
			)
		}
		defer releaseBudget()
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
	mux.HandleFunc(coreinfra.TypeResourceValidation, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.ValidationUseCase == nil {
			return nil
		}
		var payload coreapp.ResourceValidationTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode resource validation task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.JobID != 0 && payload.DispatchToken != "" && module.BackgroundDispatch != nil {
			admitted, release := module.BackgroundDispatch.TryAcquireExecution(ctx, coreinfra.ResourceValidationQueueName)
			if !admitted {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if err := module.ValidationUseCase.ReleaseDispatch(releaseCtx, payload); err != nil {
					return fmt.Errorf("release resource validation after admission denial: %w", err)
				}
				return nil
			}
			defer release()
		}

		slog.Info(
			"processing resource validation task",
			"job_id", payload.JobID,
			"request_id", payload.RequestID,
		)
		if err := module.ValidationUseCase.Process(ctx, payload); err != nil {
			slog.Warn(
				"resource validation task failed",
				"job_id", payload.JobID,
				"request_id", payload.RequestID,
				"error", err,
			)
			if coreapp.IsNonRetryableValidationError(err) {
				return fmt.Errorf("non-retryable resource validation task failure: %w: %w", err, asynq.SkipRetry)
			}
			return err
		}
		slog.Info(
			"resource validation task finished",
			"job_id", payload.JobID,
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
			if isNonRetryableImportError(err) {
				_ = module.ImportUseCase.MarkImportFailed(ctx, payload.ImportID, "Invalid import task.")
				return fmt.Errorf("non-retryable microsoft import task failure: %w: %w", err, asynq.SkipRetry)
			}
			if finalAttempt {
				_ = module.ImportUseCase.MarkImportFailed(ctx, payload.ImportID, "Import processing failed. Please retry later.")
			}
			return err
		}
		queuedValidations, err := queueMicrosoftImportValidations(ctx, module, result)
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
			"validation_jobs_queued", queuedValidations,
		)
		return nil
	})
}

func queueMicrosoftImportValidations(ctx context.Context, module *CoreModule, result *coreapp.MicrosoftImportProcessResult) (int, error) {
	if module == nil || module.ValidationUseCase == nil || result == nil || len(result.ImportedResourceIDs) == 0 {
		return 0, nil
	}
	module.ValidationUseCase.ScheduleDispatcher(ctx, 0)
	return len(result.ImportedResourceIDs), nil
}

// StartResourceValidationDispatcher seeds the durable validation dispatcher
// until the returned cleanup function is called.
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
	module.ValidationUseCase.ScheduleDispatcher(ctx, 0)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				module.ValidationUseCase.ScheduleDispatcher(ctx, 0)
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
