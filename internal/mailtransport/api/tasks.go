package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	mailDispatcherInterval                  = 15 * time.Second
	microsoftAliasDispatcherInterval        = 2 * time.Second
	microsoftTokenRefreshDispatcherInterval = 2 * time.Second
	microsoftAliasDispatchMaximum           = 128
	microsoftTokenRefreshDispatchMaximum    = 32
	backgroundLegacyReleaseTimeout          = 5 * time.Second
	backgroundLegacyAliasRetryDelay         = 30 * time.Second
)

func RegisterMailTransportTaskHandlers(mux *asynq.ServeMux, module *MailTransportModule) {
	mux.HandleFunc(mailinfra.TypeMicrosoftTokenRefreshDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.TokenRefresh == nil {
			return nil
		}
		limit := backgroundDispatchLimit(module.BackgroundExecution, microsoftTokenRefreshDispatchMaximum)
		if limit <= 0 {
			return nil
		}
		result, err := module.TokenRefresh.DispatchPending(ctx, limit)
		if err != nil {
			slog.Warn("microsoft token refresh dispatcher deferred")
			return nil
		}
		if result != nil && result.Attempted > 0 {
			slog.Info(
				"microsoft token refresh dispatcher finished",
				"attempted", result.Attempted,
				"queued", result.Queued,
				"failed", result.Failed,
			)
		}
		return nil
	})

	mux.HandleFunc(mailinfra.TypeMicrosoftTokenRefresh, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.TokenRefresh == nil {
			return nil
		}
		var payload mailapp.MicrosoftTokenRefreshTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode microsoft token refresh task: invalid payload: %w", asynq.SkipRetry)
		}
		if payload.ResourceID == 0 || payload.Generation == 0 || payload.ExpectedCredentialRevision == 0 {
			return fmt.Errorf("decode microsoft token refresh task: identity is missing: %w", asynq.SkipRetry)
		}
		release, admitted, err := tryAcquireBackgroundExecution(ctx, module.BackgroundExecution)
		if err != nil {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return releaseMicrosoftTokenRefreshDispatch(ctx, module, payload)
			}
			return err
		}
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return releaseMicrosoftTokenRefreshDispatch(ctx, module, payload)
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.TokenRefresh.Process(ctx, payload); err != nil {
			slog.Warn(
				"microsoft token refresh task deferred",
				"resource_id", payload.ResourceID,
				"generation", payload.Generation,
				"request_id", payload.RequestID,
			)
			return err
		}
		slog.Info(
			"microsoft token refresh task finished",
			"resource_id", payload.ResourceID,
			"generation", payload.Generation,
			"request_id", payload.RequestID,
		)
		return nil
	})

	mux.HandleFunc(mailinfra.TypeMicrosoftAliasDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.MicrosoftAliases == nil {
			return nil
		}
		limit := backgroundDispatchLimit(module.BackgroundExecution, microsoftAliasDispatchMaximum)
		if limit <= 0 {
			return nil
		}
		result, err := module.MicrosoftAliases.DispatchPending(ctx, limit)
		if err != nil {
			slog.Warn("microsoft alias dispatcher failed", "error", err)
			return nil
		}
		if result != nil {
			if result.Ensured > 0 || result.Attempted > 0 {
				slog.Info(
					"microsoft alias dispatcher finished",
					"ensured", result.Ensured,
					"attempted", result.Attempted,
					"queued", result.Queued,
					"failed", result.Failed,
				)
			}
		}
		return nil
	})

	mux.HandleFunc(mailinfra.TypeMicrosoftAlias, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.MicrosoftAliases == nil {
			return nil
		}
		var payload mailapp.MicrosoftAliasTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode microsoft alias task: %w: %w", err, asynq.SkipRetry)
		}
		if payload.ResourceID == 0 || payload.Generation == 0 {
			return fmt.Errorf("decode microsoft alias task: identity is missing: %w", asynq.SkipRetry)
		}
		release, admitted, err := tryAcquireBackgroundExecution(ctx, module.BackgroundExecution)
		if err != nil {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return releaseMicrosoftAliasDispatch(ctx, module, payload)
			}
			return err
		}
		if !admitted {
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return releaseMicrosoftAliasDispatch(ctx, module, payload)
			}
			return platform.ErrBackgroundExecutionDeferred
		}
		defer release()
		if err := module.MicrosoftAliases.Process(ctx, payload); err != nil {
			slog.Warn("microsoft alias task failed", "resource_id", payload.ResourceID, "error", err)
			if module.AliasDispatch == nil {
				return err
			}
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundLegacyReleaseTimeout)
			defer cancel()
			if releaseErr := module.AliasDispatch.MarkDispatchFailed(
				recoveryCtx,
				payload,
				time.Now().UTC().Add(backgroundLegacyAliasRetryDelay),
				"Microsoft alias task infrastructure failed; dispatcher will retry.",
			); releaseErr != nil {
				return fmt.Errorf("release failed microsoft alias task after %v: %w", err, releaseErr)
			}
			return nil
		}
		slog.Info("microsoft alias task finished", "resource_id", payload.ResourceID)
		return nil
	})

	mux.HandleFunc(mailinfra.TypeOutboundDispatch, func(_ context.Context, _ *asynq.Task) error {
		// Legacy durable-outbox dispatch tasks intentionally do nothing.
		return nil
	})

	mux.HandleFunc(mailinfra.TypeOutboundSend, func(ctx context.Context, task *asynq.Task) error {
		return processOutboundSendTask(ctx, task, module)
	})

	mux.HandleFunc(mailinfra.TypeInboundDispatch, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.InboundUseCase == nil {
			return nil
		}
		result, err := module.InboundUseCase.DispatchPending(ctx, 0)
		if err != nil {
			slog.Warn("inbound mail dispatcher failed", "error", err)
			return err
		}
		if result != nil && result.Attempted > 0 {
			slog.Info("inbound mail dispatcher finished", "attempted", result.Attempted, "queued", result.Queued, "failed", result.Failed)
		}
		return nil
	})

	mux.HandleFunc(mailinfra.TypeInboundProcess, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.InboundUseCase == nil {
			return nil
		}
		var payload mailapp.InboundProcessTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode inbound mail task: %w: %w", err, asynq.SkipRetry)
		}
		finalAttempt := isFinalAttempt(ctx)
		if err := module.InboundUseCase.Process(ctx, payload, finalAttempt); err != nil {
			slog.Warn(
				"inbound mail task failed",
				"inbound_mail_id", payload.InboundMailID,
				"final_attempt", finalAttempt,
				"error", err,
			)
			return err
		}
		slog.Info("inbound mail task finished", "inbound_mail_id", payload.InboundMailID)
		return nil
	})
}

func processOutboundSendTask(ctx context.Context, task *asynq.Task, module *MailTransportModule) (err error) {
	if module == nil || module.OutboundSendUseCase == nil {
		return nil
	}
	cleanupID := ""
	defer func() {
		if cleanupID != "" && module.OutboundQueue != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundLegacyReleaseTimeout)
			defer cancel()
			if cleanupErr := module.OutboundQueue.DeleteOutboundSend(cleanupCtx, cleanupID); cleanupErr != nil {
				slog.Warn("outbound mail payload cleanup failed", "error", cleanupErr)
			}
		}
		if recovered := recover(); recovered != nil {
			slog.Error("outbound mail task panicked")
			err = asynq.RevokeTask
		}
	}()
	var payload mailapp.OutboundSendTask
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		slog.Warn("outbound mail task discarded", "reason", "invalid payload")
		return nil
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID != "" {
		if module.OutboundQueue == nil {
			slog.Warn("outbound mail task discarded", "reason", "payload store unavailable")
			return nil
		}
		cleanupID = payload.ID
		stored, found, loadErr := module.OutboundQueue.LoadOutboundSend(ctx, payload.ID)
		if loadErr != nil {
			slog.Warn("outbound mail task discarded", "reason", "payload read failed", "error", loadErr)
			return nil
		}
		if !found {
			slog.Warn("outbound mail task discarded", "reason", "payload expired")
			return nil
		}
		payload = stored
	}
	if err := module.OutboundSendUseCase.Process(ctx, payload); err != nil {
		slog.Warn("outbound mail task failed", "purpose", payload.Message.Purpose, "error", err)
		return nil
	}
	slog.Info("outbound mail task finished", "purpose", payload.Message.Purpose)
	return nil
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

func isFinalAttempt(ctx context.Context) bool {
	retried, retryOK := asynq.GetRetryCount(ctx)
	maxRetry, maxRetryOK := asynq.GetMaxRetry(ctx)
	return retryOK && maxRetryOK && retried >= maxRetry
}

func releaseMicrosoftTokenRefreshDispatch(ctx context.Context, module *MailTransportModule, task mailapp.MicrosoftTokenRefreshTask) error {
	if module == nil || module.TokenRefresh == nil {
		return nil
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundLegacyReleaseTimeout)
	defer cancel()
	if err := module.TokenRefresh.ReleaseDispatch(recoveryCtx, task); err != nil {
		return fmt.Errorf("release legacy microsoft token refresh dispatch: %w", err)
	}
	return nil
}

func releaseMicrosoftAliasDispatch(ctx context.Context, module *MailTransportModule, task mailapp.MicrosoftAliasTask) error {
	if module == nil || module.AliasDispatch == nil {
		return fmt.Errorf("release legacy microsoft alias dispatch: schedule store is unavailable")
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundLegacyReleaseTimeout)
	defer cancel()
	if err := module.AliasDispatch.MarkDispatchFailed(
		recoveryCtx,
		task,
		time.Now().UTC().Add(backgroundLegacyAliasRetryDelay),
		"",
	); err != nil {
		return fmt.Errorf("release legacy microsoft alias dispatch: %w", err)
	}
	return nil
}

func scheduleMicrosoftAliasDispatcher(ctx context.Context, module *MailTransportModule, delay time.Duration) {
	if module == nil {
		return
	}
	if module.MicrosoftAliases != nil {
		module.MicrosoftAliases.ScheduleDispatcher(ctx, delay)
	}
}

func scheduleMicrosoftTokenRefreshDispatcher(ctx context.Context, module *MailTransportModule, delay time.Duration) {
	if module == nil {
		return
	}
	if module.TokenRefresh != nil {
		module.TokenRefresh.ScheduleDispatcher(ctx, delay)
	}
}
