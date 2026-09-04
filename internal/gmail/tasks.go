package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const (
	typeGmailDispatch             = "gmail:dispatch"
	typeGmailResourceImport       = "gmail:resource_import"
	gmailDispatchPeriod           = 5 * time.Second
	gmailValidationDispatchPeriod = 30 * time.Second
	gmailHistoryDispatchPeriod    = 15 * time.Second
	gmailHistoryConcurrency       = 4
	gmailHistoryMaxConcurrency    = 8096
	gmailValidationConcurrency    = 2
	gmailValidationMaxConcurrency = 64
)

var (
	localGmailHistoryActive    atomic.Int64
	localGmailValidationActive atomic.Int64
)

func (s *Service) scheduleDispatcher(ctx context.Context) error {
	if s == nil || s.queue == nil {
		return errors.New("gmail: task queue unavailable")
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailDispatch, nil),
		asynq.Queue(platform.QueueBackgroundInventory), asynq.Unique(gmailDispatchPeriod),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(30*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func RegisterTaskHandlers(mux *asynq.ServeMux, service *Service) func(context.Context) {
	mux.HandleFunc(typeGmailDispatch, func(ctx context.Context, _ *asynq.Task) error {
		return service.DispatchGmailResourceImports(ctx, 100)
	})
	mux.HandleFunc(typeGmailValidationDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if err := service.DispatchLocalResourceValidations(ctx, localGmailValidationBatchMax); err != nil {
			slog.Warn("Gmail resource validation dispatcher failed", "error", err)
		}
		return nil
	})
	mux.HandleFunc(typeGmailValidationBatch, func(ctx context.Context, task *asynq.Task) error {
		var payload localGmailValidationBatchTask
		if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.BatchID == "" || payload.ClaimToken == "" || payload.Cursor < 0 {
			return fmt.Errorf("decode Gmail validation batch task: %w", asynq.SkipRetry)
		}
		release, admitted := platform.AcquireBackgroundExecution(ctx, service.backgroundExecution)
		if !admitted {
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		return service.ProcessLocalResourceValidationBatch(ctx, payload)
	})
	mux.HandleFunc(typeGmailValidateLocal, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeLocalResourceValidationTask(task)
		if err != nil {
			return err
		}
		release, admitted := acquireLocalGmailValidationCapacity(ctx, service)
		if !admitted {
			if platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return platform.ErrBackgroundExecutionDeferred
			}
			return service.ReleaseLocalResourceValidation(context.WithoutCancel(ctx), payload)
		}
		defer release()
		if err := service.ProcessLocalResourceValidation(ctx, payload); err != nil {
			if platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return err
			}
			if releaseErr := service.ReleaseLocalResourceValidation(context.WithoutCancel(ctx), payload); releaseErr != nil {
				return errors.Join(err, releaseErr)
			}
			return nil
		}
		_ = service.scheduleLocalResourceValidationDispatcher(context.WithoutCancel(ctx), time.Second)
		return nil
	})
	mux.HandleFunc(typeGmailValidatedHistoryScan, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeLocalGmailHistoryTask(task)
		if err != nil {
			return err
		}
		release, admitted := acquireLocalGmailHistoryCapacity(ctx, service)
		if !admitted {
			if platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return platform.ErrBackgroundExecutionDeferred
			}
			return nil
		}
		defer release()
		if err := service.ProcessValidatedLocalGmailHistory(ctx, payload); err != nil {
			if !errors.Is(err, platform.ErrBackgroundExecutionDeferred) {
				slog.Warn(
					"validated Gmail history scan task failed",
					"resource_id", payload.ResourceID,
					"request_id", payload.RequestID,
					"error", err,
				)
			}
			if !errors.Is(err, platform.ErrBackgroundExecutionDeferred) && !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				if finishErr := service.finishGmailMaintenanceRunForTask(
					context.WithoutCancel(ctx), payload.MaintenanceRunID, payload.ResourceID,
					payload.ValidationGeneration, gmailMaintenanceHistory, gmailMaintenanceFailed,
					"Gmail history scanning failed after its retry budget was exhausted.",
				); finishErr != nil {
					return errors.Join(err, finishErr)
				}
				return nil
			}
			return err
		}
		return nil
	})
	mux.HandleFunc(typeGmailProjectHistoryDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		return errors.Join(
			service.DispatchLocalGmailProjectHistory(ctx, localGmailProjectHistoryLimit),
			service.dispatchIdentifyingLocalGmailHistory(ctx, localGmailValidationBatchMax),
		)
	})
	mux.HandleFunc(typeGmailProjectHistoryScan, func(ctx context.Context, task *asynq.Task) error {
		var payload localGmailProjectHistoryTask
		if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.ProjectID == 0 || payload.Generation == 0 {
			return fmt.Errorf("decode Gmail project history task: %w", asynq.SkipRetry)
		}
		release, admitted := acquireLocalGmailHistoryCapacity(ctx, service)
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				return service.ReleaseLocalGmailProjectHistoryDispatch(releaseCtx, payload)
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := service.ProcessLocalGmailProjectHistory(ctx, payload); err != nil {
			slog.Warn("Gmail project history scan task failed", "project_id", payload.ProjectID, "generation", payload.Generation, "error", err)
			return err
		}
		return nil
	})
	mux.HandleFunc(typeGmailResourceImport, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeGmailResourceImportTask(task)
		if err != nil {
			return err
		}
		record, _ := service.gmailResourceImportRecord(ctx, payload.ImportID)
		ownerUserID := uint(0)
		requestID := ""
		if record != nil {
			ownerUserID, requestID = record.OwnerUserID, record.RequestID
		}
		slog.Info(
			"processing Gmail resource import task",
			"import_id", payload.ImportID,
			"owner_user_id", ownerUserID,
			"request_id", requestID,
		)
		err = service.ProcessGmailResourceImport(ctx, payload)
		if err == nil {
			finished, _ := service.gmailResourceImportRecord(ctx, payload.ImportID)
			validationsPending := 0
			if finished != nil && finished.Status == "imported" {
				validationsPending = finished.Imported
			}
			slog.Info(
				"Gmail resource import task finished",
				"import_id", payload.ImportID,
				"owner_user_id", ownerUserID,
				"request_id", requestID,
				"validations_pending", validationsPending,
			)
			return nil
		}
		finalAttempt := gmailImportFinalAttempt(ctx)
		slog.Warn(
			"Gmail resource import task failed",
			"import_id", payload.ImportID,
			"owner_user_id", ownerUserID,
			"request_id", requestID,
			"final_attempt", finalAttempt,
			"error", err,
		)
		if errors.Is(err, ErrGmailImportInvalidCommand) || errors.Is(err, ErrGmailImportInvalidClaim) {
			return fmt.Errorf("discard Gmail resource import task: %w: %w", err, asynq.SkipRetry)
		}
		if !finalAttempt {
			return err
		}
		releaseErr := service.ReleaseGmailResourceImport(ctx, payload, "Import infrastructure is temporarily unavailable; dispatcher will retry.")
		if releaseErr != nil && !errors.Is(releaseErr, ErrGmailImportInvalidClaim) {
			return errors.Join(err, releaseErr)
		}
		return err
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastDispatch := time.Time{}
		lastValidationDispatch := time.Time{}
		lastHistoryDispatch := time.Time{}
		for {
			now := time.Now()
			if lastDispatch.IsZero() || now.Sub(lastDispatch) >= gmailDispatchPeriod {
				if err := service.scheduleDispatcher(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("schedule Gmail dispatcher failed", "error", err)
				}
				lastDispatch = now
			}
			if lastValidationDispatch.IsZero() || now.Sub(lastValidationDispatch) >= gmailValidationDispatchPeriod {
				if err := service.scheduleLocalResourceValidationDispatcher(ctx, 0); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("schedule Gmail validation dispatcher failed", "error", err)
				}
				lastValidationDispatch = now
			}
			if lastHistoryDispatch.IsZero() || now.Sub(lastHistoryDispatch) >= gmailHistoryDispatchPeriod {
				if err := service.scheduleLocalGmailProjectHistoryDispatcher(ctx, 0); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("schedule Gmail project history dispatcher failed", "error", err)
				}
				lastHistoryDispatch = now
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
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

func acquireLocalGmailValidationCapacity(ctx context.Context, service *Service) (func(), bool) {
	backgroundRelease, admitted := platform.AcquireBackgroundExecution(ctx, service.backgroundExecution)
	if !admitted {
		return func() {}, false
	}
	limit := int64(min(runtimeconfig.Int("gmail_validation_concurrency", gmailValidationConcurrency, 1), gmailValidationMaxConcurrency))
	for {
		active := localGmailValidationActive.Load()
		if active >= limit {
			backgroundRelease()
			return func() {}, false
		}
		if localGmailValidationActive.CompareAndSwap(active, active+1) {
			return func() {
				localGmailValidationActive.Add(-1)
				backgroundRelease()
			}, true
		}
	}
}

func acquireLocalGmailHistoryCapacity(ctx context.Context, service *Service) (func(), bool) {
	backgroundRelease, admitted := platform.AcquireBackgroundExecution(ctx, service.backgroundExecution)
	if !admitted {
		return func() {}, false
	}
	limit := int64(min(runtimeconfig.Int("gmail_history_concurrency", gmailHistoryConcurrency, 1), gmailHistoryMaxConcurrency))
	for {
		active := localGmailHistoryActive.Load()
		if active >= limit {
			backgroundRelease()
			return func() {}, false
		}
		if localGmailHistoryActive.CompareAndSwap(active, active+1) {
			return func() {
				localGmailHistoryActive.Add(-1)
				backgroundRelease()
			}, true
		}
	}
}

func decodeGmailResourceImportTask(task *asynq.Task) (gmailResourceImportTask, error) {
	var payload gmailResourceImportTask
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.ImportID == 0 || payload.Generation == 0 {
		return gmailResourceImportTask{}, fmt.Errorf("decode Gmail resource import task: %w", asynq.SkipRetry)
	}
	return payload, nil
}

func gmailImportFinalAttempt(ctx context.Context) bool {
	retried, retryOK := asynq.GetRetryCount(ctx)
	maximum, maximumOK := asynq.GetMaxRetry(ctx)
	return retryOK && maximumOK && retried >= maximum
}
