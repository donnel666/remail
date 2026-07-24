package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const outboundSendRetries = 3

type OutboundMailQueue interface {
	EnqueueOutboundSend(ctx context.Context, task OutboundSendTask) (bool, error)
}

// OutboundSendTask normally carries only ID. Message remains for draining
// tasks created by the previous deployment.
type OutboundSendTask struct {
	ID         string                 `json:"id,omitempty"`
	Message    domain.OutboundMessage `json:"message,omitempty"`
	RetryCount *int                   `json:"retryCount,omitempty"`
}

type AsyncDeliveryService struct {
	queue OutboundMailQueue
	from  string
}

func NewAsyncDeliveryService(queue OutboundMailQueue, from string) *AsyncDeliveryService {
	return &AsyncDeliveryService{queue: queue, from: strings.TrimSpace(from)}
}

func (s *AsyncDeliveryService) Send(ctx context.Context, message domain.OutboundMessage) error {
	message = s.normalizeOutboundMessage(message)
	if s.queue == nil {
		return deliveryUnavailable("outbound mail queue unavailable", nil)
	}
	if _, err := s.queue.EnqueueOutboundSend(ctx, OutboundSendTask{Message: message}); err != nil {
		if errors.Is(err, domain.ErrOutboundIdempotencyConflict) {
			return err
		}
		return deliveryUnavailable("outbound mail enqueue failed", err)
	}
	return nil
}

type OutboundSendUseCase struct {
	sender     SenderPort
	retryDelay func(int) time.Duration
}

func NewOutboundSendUseCase(sender SenderPort) *OutboundSendUseCase {
	return &OutboundSendUseCase{
		sender:     sender,
		retryDelay: func(attempt int) time.Duration { return time.Duration(attempt) * time.Second },
	}
}

func (uc *OutboundSendUseCase) Process(ctx context.Context, task OutboundSendTask) error {
	message := normalizeOutboundMessage(task.Message)
	if message.IdempotencyKey == "" || message.To == "" {
		return fmt.Errorf("%w: outbound mail task invalid", domain.ErrDeliveryUnavailable)
	}
	if uc.sender == nil {
		return deliveryUnavailable("outbound mail sender unavailable", nil)
	}
	retries := min(runtimeconfig.Int("smtp_task_retry_count", outboundSendRetries, 0), 20)
	if task.RetryCount != nil {
		retries = min(max(*task.RetryCount, 0), 20)
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return deliveryUnavailable("outbound mail send interrupted", err)
		}
		err := uc.sender.Send(ctx, message)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return deliveryUnavailable("outbound mail send interrupted", err)
		}
		var failure *OutboundSendFailure
		if errors.As(err, &failure) && !failure.Retryable {
			return deliveryUnavailable("outbound mail send failed", err)
		}
		if attempt == retries {
			return deliveryUnavailable("outbound mail send failed", err)
		}
		delay := time.Duration(attempt+1) * time.Second
		if uc.retryDelay != nil {
			delay = uc.retryDelay(attempt + 1)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return deliveryUnavailable("outbound mail send interrupted", ctx.Err())
		case <-timer.C:
		}
	}
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
