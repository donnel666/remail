package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	"github.com/hibiken/asynq"
)

// StartCoreWorkers registers Core task handlers and starts the Asynq server.
func StartCoreWorkers(server *asynq.Server, module *CoreModule) error {
	mux := asynq.NewServeMux()
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
		if err := module.ImportUseCase.ProcessMicrosoftImport(ctx, payload); err != nil {
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
		slog.Info(
			"microsoft import task finished",
			"import_id", payload.ImportID,
			"owner_user_id", payload.OwnerUserID,
			"request_id", payload.RequestID,
		)
		return nil
	})

	return server.Start(mux)
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
