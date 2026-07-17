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
	EnqueueOutboundSend(ctx context.Context, task OutboundSendTask) error
	EnqueueOutboundDispatch(ctx context.Context, delay time.Duration) error
}

type OutboundSendTask struct {
	IdempotencyKey string `json:"idempotencyKey"`
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
			if err := s.store.MarkPending(ctx, reserved.IdempotencyKey, "Outbound mail sending stale; queued for retry."); err != nil {
				return deliveryUnavailable("outbound mail reset pending failed", err)
			}
		case domain.OutboundStatusPending:
		case domain.OutboundStatusFailed:
			if err := s.store.MarkPending(ctx, reserved.IdempotencyKey, ""); err != nil {
				return deliveryUnavailable("outbound mail reset pending failed", err)
			}
		default:
			return deliveryUnavailable("outbound mail status invalid", nil)
		}
	}

	if err := s.enqueueOutbound(ctx, reserved.IdempotencyKey); err != nil {
		_ = s.store.MarkPending(ctx, reserved.IdempotencyKey, "Outbound mail enqueue failed.")
		writeSystemLog(ctx, s.logs, "error", "mail.outbound_enqueue_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail task could not be queued.", err)
		return nil
	}
	return nil
}

func (s *AsyncDeliveryService) DispatchPending(ctx context.Context, limit int) (*OutboundDispatchResult, error) {
	if limit <= 0 {
		limit = 100
	}
	mails, err := s.store.ClaimDispatchable(ctx, limit, s.now().Add(-outboundMailClaimTimeout))
	if err != nil {
		return nil, deliveryUnavailable("outbound mail dispatch claim failed", err)
	}
	result := &OutboundDispatchResult{Attempted: len(mails)}
	for _, mail := range mails {
		if err := s.enqueueOutbound(ctx, mail.IdempotencyKey); err != nil {
			result.Failed++
			_ = s.store.MarkPending(ctx, mail.IdempotencyKey, "Outbound mail enqueue failed.")
			writeSystemLog(ctx, s.logs, "error", "mail.outbound_dispatch_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail dispatcher could not queue task.", err)
			continue
		}
		result.Queued++
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

func (s *AsyncDeliveryService) enqueueOutbound(ctx context.Context, idempotencyKey string) error {
	if s.queue == nil {
		return fmt.Errorf("outbound mail queue is unavailable")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return fmt.Errorf("outbound mail idempotency key is empty")
	}
	return s.queue.EnqueueOutboundSend(ctx, OutboundSendTask{IdempotencyKey: idempotencyKey})
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
	if uc.sender == nil {
		return deliveryUnavailable("outbound mail sender unavailable", nil)
	}
	idempotencyKey := strings.TrimSpace(task.IdempotencyKey)
	if idempotencyKey == "" {
		return fmt.Errorf("%w: outbound mail task invalid", domain.ErrDeliveryUnavailable)
	}

	now := uc.now()
	mail, claimed, err := uc.store.ClaimSending(ctx, idempotencyKey, now.Add(-outboundMailClaimTimeout), now)
	if err != nil {
		return deliveryUnavailable("outbound mail mark sending failed", err)
	}
	if !claimed || mail == nil {
		return nil
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
		reason := safeDiagnostic(err.Error())
		if finalAttempt {
			_ = uc.store.MarkFailed(ctx, mail.IdempotencyKey, reason)
			writeSystemLog(ctx, uc.logs, "error", "mail.outbound_failed", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail sending failed.", err)
		} else {
			_ = uc.store.MarkPending(ctx, mail.IdempotencyKey, reason)
			writeSystemLog(ctx, uc.logs, "warning", "mail.outbound_retry", "", "outbound_mail", mailLogID(mail.IdempotencyKey), "Outbound mail sending will retry.", err)
		}
		return err
	}

	_ = uc.store.MarkSent(ctx, mail.IdempotencyKey, uc.now())
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
