package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/hibiken/asynq"
)

const (
	mailDispatcherInterval            = 15 * time.Second
	microsoftAliasDispatcherInterval  = 15 * time.Second
	microsoftAliasDispatchMinimum     = 4
	microsoftAliasDispatchMaximum     = 64
	microsoftAliasAdmissionRetryDelay = 30 * time.Second
)

func RegisterMailTransportTaskHandlers(mux *asynq.ServeMux, module *MailTransportModule) {
	mux.HandleFunc(mailinfra.TypeMicrosoftAliasDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.MicrosoftAliases == nil {
			return nil
		}
		limit := microsoftAliasDispatchMaximum
		releaseBudget := func() {}
		if module.BackgroundDispatch != nil {
			limit, releaseBudget = module.BackgroundDispatch.AcquireDispatchBudget(
				ctx,
				mailinfra.MicrosoftAliasQueueName,
				microsoftAliasDispatchMinimum,
				microsoftAliasDispatchMaximum,
			)
		}
		defer releaseBudget()
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
		if payload.ResourceID == 0 || payload.DispatchToken == "" {
			return fmt.Errorf("decode microsoft alias task: identity is missing: %w", asynq.SkipRetry)
		}
		if module.BackgroundDispatch != nil {
			admitted, release := module.BackgroundDispatch.TryAcquireExecution(ctx, mailinfra.MicrosoftAliasQueueName)
			if !admitted {
				if module.AliasDispatch == nil {
					slog.Warn("microsoft alias task admission deferred without schedule releaser", "resource_id", payload.ResourceID)
					return nil
				}
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if err := module.AliasDispatch.MarkDispatchFailed(
					releaseCtx,
					payload,
					time.Now().UTC().Add(microsoftAliasAdmissionRetryDelay),
					"",
				); err != nil {
					return fmt.Errorf("defer microsoft alias task after admission denial: %w", err)
				}
				return nil
			}
			defer release()
		}
		if err := module.MicrosoftAliases.Process(ctx, payload); err != nil {
			slog.Warn("microsoft alias task failed", "resource_id", payload.ResourceID, "error", err)
			return err
		}
		slog.Info("microsoft alias task finished", "resource_id", payload.ResourceID)
		return nil
	})

	mux.HandleFunc(mailinfra.TypeOutboundDispatch, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.OutboundDelivery == nil {
			return nil
		}
		defer module.OutboundDelivery.ScheduleDispatcher(context.Background(), mailDispatcherInterval)
		result, err := module.OutboundDelivery.DispatchPending(ctx, 0)
		if err != nil {
			slog.Warn("outbound mail dispatcher failed", "error", err)
			return err
		}
		if result != nil && result.Attempted > 0 {
			slog.Info("outbound mail dispatcher finished", "attempted", result.Attempted, "queued", result.Queued, "failed", result.Failed)
		}
		return nil
	})

	mux.HandleFunc(mailinfra.TypeOutboundSend, func(ctx context.Context, task *asynq.Task) error {
		if module == nil || module.OutboundSendUseCase == nil {
			return nil
		}
		var payload mailapp.OutboundSendTask
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode outbound mail task: %w: %w", err, asynq.SkipRetry)
		}
		finalAttempt := isFinalAttempt(ctx)
		if err := module.OutboundSendUseCase.Process(ctx, payload, finalAttempt); err != nil {
			slog.Warn(
				"outbound mail task failed",
				"final_attempt", finalAttempt,
				"error", err,
			)
			return err
		}
		slog.Info(
			"outbound mail task finished",
		)
		return nil
	})

	mux.HandleFunc(mailinfra.TypeInboundDispatch, func(ctx context.Context, _ *asynq.Task) error {
		if module == nil || module.InboundUseCase == nil {
			return nil
		}
		defer module.InboundUseCase.ScheduleDispatcher(context.Background(), mailDispatcherInterval)
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

func isFinalAttempt(ctx context.Context) bool {
	retried, retryOK := asynq.GetRetryCount(ctx)
	maxRetry, maxRetryOK := asynq.GetMaxRetry(ctx)
	return retryOK && maxRetryOK && retried >= maxRetry
}

func scheduleMailDispatchers(ctx context.Context, module *MailTransportModule, delay time.Duration) {
	if module == nil {
		return
	}
	if module.OutboundDelivery != nil {
		module.OutboundDelivery.ScheduleDispatcher(ctx, delay)
	}
	if module.InboundUseCase != nil {
		module.InboundUseCase.ScheduleDispatcher(ctx, delay)
	}
}

func scheduleMicrosoftAliasDispatcher(ctx context.Context, module *MailTransportModule, delay time.Duration) {
	if module == nil {
		return
	}
	if module.MicrosoftAliases != nil {
		module.MicrosoftAliases.ScheduleDispatcher(ctx, delay)
	}
}
