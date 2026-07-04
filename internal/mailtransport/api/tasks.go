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

const mailDispatcherInterval = 15 * time.Second

func RegisterMailTransportTaskHandlers(mux *asynq.ServeMux, module *MailTransportModule) {
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
