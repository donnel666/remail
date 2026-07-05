package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	"github.com/hibiken/asynq"
)

const resourceValidationDispatcherInterval = 15 * time.Second

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
		defer module.ValidationUseCase.ScheduleDispatcher(context.Background(), resourceValidationDispatcherInterval)
		result, err := module.ValidationUseCase.DispatchPending(ctx, 0)
		if err != nil {
			slog.Warn("resource validation dispatcher failed", "error", err)
			return err
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
	if module != nil && module.ValidationUseCase != nil {
		module.ValidationUseCase.ScheduleDispatcher(context.Background(), 0)
		startResourceValidationDispatcherSeeder(module)
	}

	mux.HandleFunc(coreinfra.TypeResourceValidation, func(ctx context.Context, task *asynq.Task) error {
		var payload coreapp.ResourceValidationTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode resource validation task: %w: %w", err, asynq.SkipRetry)
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
		queuedValidations, err := queueMicrosoftImportValidations(ctx, module, payload, result)
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

func queueMicrosoftImportValidations(ctx context.Context, module *CoreModule, payload coreapp.MicrosoftImportTask, result *coreapp.MicrosoftImportProcessResult) (int, error) {
	if module == nil || module.ValidationUseCase == nil || result == nil || len(result.ImportedResourceIDs) == 0 {
		return 0, nil
	}
	validationResult, err := module.ValidationUseCase.CreateBatch(ctx, coreapp.ResourceBulkSelection{
		Mode:        coreapp.ResourceBulkSelectionIDs,
		ResourceIDs: result.ImportedResourceIDs,
	}, payload.OwnerUserID, payload.RequestID, "/v1/resources/imports")
	if err != nil {
		return 0, err
	}
	if validationResult == nil {
		return 0, nil
	}
	return validationResult.Queued, nil
}

func startResourceValidationDispatcherSeeder(module *CoreModule) {
	if module == nil || module.ValidationUseCase == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(resourceValidationDispatcherInterval)
		defer ticker.Stop()
		for range ticker.C {
			module.ValidationUseCase.ScheduleDispatcher(context.Background(), 0)
		}
	}()
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
