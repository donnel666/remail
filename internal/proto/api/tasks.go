package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra"
	"github.com/hibiken/asynq"
)

func RegisterTaskHandlers(mux *asynq.ServeMux, module *Module) func(context.Context) {
	if mux == nil || module == nil || module.Service == nil {
		return func(context.Context) {}
	}
	service := module.Service
	register := func(kind string, handle func(context.Context, *asynq.Task) error) {
		mux.HandleFunc(kind, func(ctx context.Context, task *asynq.Task) error {
			switch kind {
			case app.TaskImportDispatcher, app.TaskValidationDispatcher, app.TaskHistoryDispatcher, app.TaskProjectHistoryDispatcher:
				return handle(ctx, task)
			}
			release, admitted := platform.AcquireBackgroundExecution(ctx, service.BackgroundExecution)
			if !admitted {
				if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
					return releaseExhaustedTask(ctx, service, task)
				}
				return platform.ErrBackgroundExecutionDeferred
			}
			defer release()
			err := handle(ctx, task)
			if errors.Is(err, domain.ErrInvalidClaim) || errors.Is(err, domain.ErrResourceMissing) {
				return nil
			}
			if err != nil && !errors.Is(err, asynq.SkipRetry) && !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				if releaseErr := releaseExhaustedTask(ctx, service, task); releaseErr != nil {
					return errors.Join(err, releaseErr)
				}
			}
			return err
		})
	}
	register(app.TaskImport, func(ctx context.Context, task *asynq.Task) error {
		payload, err := app.DecodeImportTask(task)
		if err != nil {
			return err
		}
		_, err = service.ProcessImportTask(ctx, payload.ImportID, payload.Generation)
		if err != nil {
			return err
		}
		if module.Queue != nil {
			return app.EnqueueValidationDispatcher(ctx, module.Queue)
		}
		return nil
	})
	register(app.TaskImportDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		return service.DispatchPendingImports(ctx, module.Queue, 100)
	})
	register(app.TaskValidationDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		_, err := service.DispatchPendingValidations(ctx, module.Queue, 100)
		return err
	})
	register(app.TaskHistoryDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		_, err := service.DispatchPendingHistory(ctx, module.Queue, 100)
		return err
	})
	register(app.TaskValidate, func(ctx context.Context, task *asynq.Task) error {
		payload, err := app.DecodeValidationTask(task)
		if err != nil {
			return err
		}
		if err := service.ProcessValidation(ctx, payload); err != nil {
			return err
		}
		writeSystemLog(ctx, module, payload.RequestID, "validation", payload.ResourceID, "Proto validation task completed; inspect the maintenance result for its outcome.")
		if module.Queue != nil {
			return app.EnqueueHistoryDispatcher(ctx, module.Queue)
		}
		return nil
	})
	register(app.TaskHistory, func(ctx context.Context, task *asynq.Task) error {
		payload, err := app.DecodeHistoryTask(task)
		if err != nil {
			return err
		}
		if err := service.ProcessHistory(ctx, payload); err != nil {
			return err
		}
		writeSystemLog(ctx, module, payload.RequestID, "history", payload.ResourceID, "Proto history task completed; inspect the maintenance result for its outcome.")
		return nil
	})
	register(app.TaskBulk, func(ctx context.Context, task *asynq.Task) error {
		var payload infra.BulkTask
		if json.Unmarshal(task.Payload(), &payload) != nil || payload.TaskID == "" || payload.BatchID == "" || payload.ClaimToken == "" {
			return asynq.SkipRetry
		}
		if err := service.ProcessBulk(ctx, payload); err != nil {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if failureErr := service.FailBulk(cleanup, payload); failureErr != nil {
					return errors.Join(err, failureErr)
				}
			}
			return err
		}
		if module.Queue != nil {
			_ = app.EnqueueValidationDispatcher(ctx, module.Queue)
			_ = app.EnqueueHistoryDispatcher(ctx, module.Queue)
		}
		return nil
	})
	register(app.TaskProjectHistory, func(ctx context.Context, task *asynq.Task) error {
		var payload infra.ProjectHistoryTask
		if json.Unmarshal(task.Payload(), &payload) != nil || payload.ProjectID == 0 || payload.Generation == 0 {
			return asynq.SkipRetry
		}
		return service.ProcessProjectHistory(ctx, payload)
	})
	register(app.TaskProjectHistoryDispatcher, func(ctx context.Context, _ *asynq.Task) error { return service.DispatchProjectHistory(ctx, 100) })
	if module.Queue == nil {
		return func(context.Context) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	seed := func() {
		callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		for _, enqueue := range []func(context.Context, app.Queue) error{app.EnqueueImportDispatcher, app.EnqueueValidationDispatcher, app.EnqueueHistoryDispatcher} {
			if err := enqueue(callCtx, module.Queue); err != nil {
				slog.Warn("Proto dispatcher scheduling failed", "error", err)
			}
		}
		if err := service.DispatchProjectHistory(callCtx, 100); err != nil {
			slog.Warn("Proto project history scheduling failed", "error", err)
		}
		if err := service.DispatchPendingBulk(callCtx); err != nil {
			slog.Warn("Proto bulk scheduling failed", "error", err)
		}
	}
	go func() {
		defer close(done)
		seed()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				seed()
			}
		}
	}()
	return func(shutdown context.Context) {
		cancel()
		select {
		case <-done:
		case <-shutdown.Done():
		}
	}
}

func releaseExhaustedTask(ctx context.Context, service *infra.Service, task *asynq.Task) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	switch task.Type() {
	case app.TaskValidate:
		payload, decodeErr := app.DecodeValidationTask(task)
		if decodeErr != nil {
			return decodeErr
		}
		err = service.ReleaseMaintenanceAssignment(cleanup, payload.ResourceID, payload.ValidationGeneration, payload.CredentialRevision, "validation")
	case app.TaskHistory:
		payload, decodeErr := app.DecodeHistoryTask(task)
		if decodeErr != nil {
			return decodeErr
		}
		err = service.ReleaseMaintenanceAssignment(cleanup, payload.ResourceID, payload.ValidationGeneration, payload.CredentialRevision, "history")
	case app.TaskBulk:
		var payload infra.BulkTask
		if decodeErr := json.Unmarshal(task.Payload(), &payload); decodeErr != nil {
			return asynq.SkipRetry
		}
		err = service.FailBulk(cleanup, payload)
	}
	if errors.Is(err, domain.ErrInvalidClaim) || errors.Is(err, domain.ErrResourceMissing) {
		return nil
	}
	return err
}
func writeSystemLog(ctx context.Context, module *Module, requestID, event string, resourceID uint, message string) {
	if module.SystemLogs != nil {
		if err := module.SystemLogs.Create(ctx, &governancedomain.SystemLog{Level: "info", Module: "proto", EventType: "proto_" + event, RequestID: requestID, BizType: "proto_resource", BizID: fmt.Sprint(resourceID), Message: message}); err != nil {
			slog.Warn("Proto system log write failed", "resource_id", resourceID, "error", err)
		}
	}
}
