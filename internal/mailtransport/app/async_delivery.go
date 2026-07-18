package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
)

type OutboundMailQueue interface {
	EnqueueOutboundSend(ctx context.Context, task OutboundSendTask) (bool, error)
	EnqueueOutboundDispatch(ctx context.Context, delay time.Duration) error
}

type OutboundSendTask struct {
	IdempotencyKey string `json:"idempotencyKey"`
	SendGeneration uint64 `json:"sendGeneration"`
}

type OutboundDispatchResult struct {
	Attempted int
	Queued    int
	Failed    int
}

type AsyncDeliveryService struct {
	store OutboundMailStore
	queue OutboundMailQueue
	logs  SystemLogPort
	from  string
	now   func() time.Time
}

func NewAsyncDeliveryService(store OutboundMailStore, queue OutboundMailQueue, logs SystemLogPort, from string) *AsyncDeliveryService {
	return &AsyncDeliveryService{
		store: store,
		queue: queue,
		logs:  logs,
		from:  strings.TrimSpace(from),
		now:   time.Now,
	}
}

func (s *AsyncDeliveryService) Send(ctx context.Context, message domain.OutboundMessage) error {
	message = s.normalizeOutboundMessage(message)
	now := s.now()
	mail := domain.NewOutboundMail(message, now)
	reserved, created, err := s.store.Reserve(ctx, mail)
	if err != nil {
		if errors.Is(err, domain.ErrOutboundIdempotencyConflict) {
			return err
		}
		return deliveryUnavailable("outbound mail reserve failed", err)
	}
	if !created {
		switch reserved.Status {
		case domain.OutboundStatusSent:
			return nil
		case domain.OutboundStatusSending:
			if !outboundMailClaimExpired(reserved, now) {
				return nil
			}
			applied, err := s.store.ReleasePending(ctx, reserved.IdempotencyKey, reserved.SendGeneration, "Outbound mail sending stale; queued for retry.")
			if err != nil {
				return deliveryUnavailable("outbound mail reset pending failed", err)
			}
			if !applied {
				return nil
			}
			reserved.SendGeneration++
		case domain.OutboundStatusPending:
		case domain.OutboundStatusFailed:
			applied, err := s.store.ResetPending(ctx, reserved.IdempotencyKey, reserved.SendGeneration, "")
			if err != nil {
				return deliveryUnavailable("outbound mail reset pending failed", err)
			}
			if !applied {
				return nil
			}
			reserved.SendGeneration++
		default:
			return deliveryUnavailable("outbound mail status invalid", nil)
		}
	}

	accepted, err := s.enqueueOutbound(ctx, reserved.IdempotencyKey, reserved.SendGeneration)
	if err != nil {
		writeSystemLog(ctx, s.logs, "error", "mail.outbound_enqueue_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail task could not be queued.", err)
		return nil
	}
	if !accepted {
		return nil
	}
	if _, err := s.store.ActivateSending(ctx, reserved.IdempotencyKey, reserved.SendGeneration, now); err != nil {
		writeSystemLog(ctx, s.logs, "error", "mail.outbound_activation_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail task was queued but could not be activated.", err)
	}
	return nil
}

func (s *AsyncDeliveryService) DispatchPending(ctx context.Context, limit int) (*OutboundDispatchResult, error) {
	if limit <= 0 {
		limit = 100
	}
	mails, err := s.store.ListPending(ctx, limit)
	if err != nil {
		return nil, deliveryUnavailable("outbound mail dispatch scan failed", err)
	}
	result := &OutboundDispatchResult{Attempted: len(mails)}
	for _, mail := range mails {
		accepted, err := s.enqueueOutbound(ctx, mail.IdempotencyKey, mail.SendGeneration)
		if err != nil {
			result.Failed++
			writeSystemLog(ctx, s.logs, "error", "mail.outbound_dispatch_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail dispatcher could not queue task.", err)
			continue
		}
		if !accepted {
			continue
		}
		result.Queued++
		if _, err := s.store.ActivateSending(ctx, mail.IdempotencyKey, mail.SendGeneration, s.now()); err != nil {
			result.Failed++
			writeSystemLog(ctx, s.logs, "error", "mail.outbound_activation_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail task was queued but could not be activated.", err)
		}
	}
	return result, nil
}

func (s *AsyncDeliveryService) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if s == nil || s.queue == nil {
		return
	}
	if err := s.queue.EnqueueOutboundDispatch(ctx, delay); err != nil {
		writeSystemLog(ctx, s.logs, "error", "mail.outbound_dispatcher_enqueue_failed", "", "outbound_mail", "dispatcher", "Outbound mail dispatcher could not be queued.", err)
	}
}

func (s *AsyncDeliveryService) enqueueOutbound(ctx context.Context, idempotencyKey string, generation uint64) (bool, error) {
	if s.queue == nil {
		return false, fmt.Errorf("outbound mail queue is unavailable")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || generation == 0 {
		return false, fmt.Errorf("outbound mail task identity is invalid")
	}
	return s.queue.EnqueueOutboundSend(ctx, OutboundSendTask{IdempotencyKey: idempotencyKey, SendGeneration: generation})
}

type OutboundSendUseCase struct {
	store  OutboundMailStore
	sender SenderPort
	logs   SystemLogPort
	now    func() time.Time
}

func NewOutboundSendUseCase(store OutboundMailStore, sender SenderPort, logs SystemLogPort) *OutboundSendUseCase {
	return &OutboundSendUseCase{
		store:  store,
		sender: sender,
		logs:   logs,
		now:    time.Now,
	}
}

func (uc *OutboundSendUseCase) Process(ctx context.Context, task OutboundSendTask, finalAttempt bool) error {
	idempotencyKey := strings.TrimSpace(task.IdempotencyKey)
	if idempotencyKey == "" || task.SendGeneration == 0 {
		return fmt.Errorf("%w: outbound mail task invalid", domain.ErrDeliveryUnavailable)
	}

	now := uc.now()
	mail, err := uc.store.FindByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail could not be loaded.", deliveryUnavailable("outbound mail find failed", err))
	}
	if mail == nil || mail.SendGeneration != task.SendGeneration || mail.Status == domain.OutboundStatusSent || mail.Status == domain.OutboundStatusFailed {
		return nil
	}
	if mail.Status == domain.OutboundStatusPending {
		activated, err := uc.store.ActivateSending(ctx, idempotencyKey, task.SendGeneration, now)
		if err != nil {
			return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail could not be activated.", deliveryUnavailable("outbound mail mark sending failed", err))
		}
		if !activated {
			mail, err = uc.store.FindByIdempotencyKey(ctx, idempotencyKey)
			if err != nil {
				return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail could not be reloaded.", deliveryUnavailable("outbound mail refind failed", err))
			}
			if mail == nil || mail.SendGeneration != task.SendGeneration || mail.Status != domain.OutboundStatusSending {
				return nil
			}
		}
	} else if mail.Status != domain.OutboundStatusSending {
		return nil
	}
	if ctx.Err() != nil {
		return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail sending interrupted.", ctx.Err())
	}
	if uc.sender == nil {
		return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail sender unavailable.", deliveryUnavailable("outbound mail sender unavailable", nil))
	}
	message := domain.OutboundMessage{
		IdempotencyKey: mail.IdempotencyKey,
		Purpose:        mail.Purpose,
		From:           mail.Sender,
		To:             mail.Recipient,
		ReplyTo:        mail.ReplyTo,
		Subject:        mail.Subject,
		TextBody:       mail.TextBody,
		HTMLBody:       mail.HTMLBody,
	}

	if err := uc.sender.Send(ctx, message); err != nil {
		if ctx.Err() != nil {
			return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail sending interrupted.", ctx.Err())
		}
		var failure *OutboundSendFailure
		if !errors.As(err, &failure) {
			return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail sender is temporarily unavailable.", err)
		}
		reason := safeDiagnostic(failure.SafeMessage)
		if reason == "" {
			reason = "Outbound mail was rejected."
		}
		terminal, applied, recordErr := uc.store.RecordSendFailure(ctx, mail.IdempotencyKey, task.SendGeneration, reason, failure.Retryable)
		if recordErr != nil {
			return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail failure state could not be stored.", errors.Join(err, deliveryUnavailable("outbound mail failure persistence failed", recordErr)))
		}
		if !applied {
			return nil
		}
		if terminal {
			writeSystemLog(ctx, uc.logs, "error", "mail.outbound_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail sending failed.", err)
		} else {
			writeSystemLog(ctx, uc.logs, "warning", "mail.outbound_retry", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail sending will retry.", err)
		}
		return nil
	}

	if _, err = uc.store.MarkSent(ctx, mail.IdempotencyKey, task.SendGeneration, uc.now()); err != nil {
		return uc.outboundInfrastructureFailure(ctx, task, finalAttempt, "Outbound mail completion could not be stored.", deliveryUnavailable("outbound mail completion persistence failed", err))
	}
	return nil
}

func (uc *OutboundSendUseCase) outboundInfrastructureFailure(ctx context.Context, task OutboundSendTask, finalAttempt bool, reason string, cause error) error {
	if !finalAttempt {
		return cause
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	applied, err := uc.store.ReleasePending(cleanupCtx, task.IdempotencyKey, task.SendGeneration, reason)
	if err != nil {
		return fmt.Errorf("%w: release outbound mail pending: %s", cause, safeDiagnostic(err.Error()))
	}
	if applied {
		writeSystemLog(cleanupCtx, uc.logs, "warning", "mail.outbound_infrastructure_released", "", "outbound_mail", mailLogID(task.IdempotencyKey), "Outbound mail was released for retry after infrastructure failure.", cause)
	}
	return nil
}

func normalizeOutboundMessage(message domain.OutboundMessage) domain.OutboundMessage {
	message.IdempotencyKey = strings.TrimSpace(message.IdempotencyKey)
	message.From = strings.TrimSpace(message.From)
	message.To = strings.TrimSpace(message.To)
	message.Subject = bodyValue(message.Subject)
	if message.IdempotencyKey == "" {
		message.IdempotencyKey = messageDigest(message.Purpose, message.From, message.To, message.Subject, message.TextBody, message.HTMLBody)
	}
	return message
}

func (s *AsyncDeliveryService) normalizeOutboundMessage(message domain.OutboundMessage) domain.OutboundMessage {
	if strings.TrimSpace(message.From) == "" {
		message.From = s.from
	}
	return normalizeOutboundMessage(message)
}
