package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
)

const (
	outboundMailTTL          = 24 * time.Hour
	outboundMailClaimTimeout = 2 * time.Minute
)

type OutboundMailStore interface {
	Reserve(ctx context.Context, mail *domain.OutboundMail, ttl time.Duration) (*domain.OutboundMail, bool, error)
	Update(ctx context.Context, mail *domain.OutboundMail, ttl time.Duration) error
}

type SenderPort interface {
	Send(ctx context.Context, message domain.OutboundMessage) error
}

type DeliveryService struct {
	store  OutboundMailStore
	sender SenderPort
	now    func() time.Time
}

func NewDeliveryService(store OutboundMailStore, sender SenderPort) *DeliveryService {
	return &DeliveryService{
		store:  store,
		sender: sender,
		now:    time.Now,
	}
}

func (s *DeliveryService) Send(ctx context.Context, message domain.OutboundMessage) error {
	message.IdempotencyKey = strings.TrimSpace(message.IdempotencyKey)
	if message.IdempotencyKey == "" {
		message.IdempotencyKey = messageDigest(message.Purpose, message.To, message.Subject, message.TextBody, message.HTMLBody)
	}

	now := s.now()
	mail := domain.NewOutboundMail(message, now)
	reserved, created, err := s.store.Reserve(ctx, mail, outboundMailTTL)
	if err != nil {
		return deliveryUnavailable("outbound mail reserve failed", err)
	}
	if !created {
		switch reserved.Status {
		case domain.OutboundStatusSent:
			return nil
		case domain.OutboundStatusSending, domain.OutboundStatusPending:
			if !outboundMailClaimExpired(reserved, now) {
				return nil
			}
		case domain.OutboundStatusFailed:
		default:
			return deliveryUnavailable("outbound mail status invalid", nil)
		}
	}
	mail = reserved
	mail.MarkSending(s.now())
	if err := s.store.Update(ctx, mail, outboundMailTTL); err != nil {
		return deliveryUnavailable("outbound mail mark sending failed", err)
	}

	if err := s.sender.Send(ctx, message); err != nil {
		mail.MarkFailed(s.now(), safeDiagnostic(err.Error()))
		_ = s.store.Update(ctx, mail, outboundMailTTL)
		if errors.Is(err, domain.ErrDeliveryUnavailable) {
			return err
		}
		return deliveryUnavailable("mail sender failed", err)
	}

	mail.MarkSent(s.now())
	_ = s.store.Update(ctx, mail, outboundMailTTL)
	return nil
}

func deliveryUnavailable(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", domain.ErrDeliveryUnavailable, stage)
	}
	return fmt.Errorf("%w: %s: %s", domain.ErrDeliveryUnavailable, stage, safeDiagnostic(err.Error()))
}

func outboundMailClaimExpired(mail *domain.OutboundMail, now time.Time) bool {
	if mail.UpdatedAt.IsZero() {
		return true
	}
	return !mail.UpdatedAt.Add(outboundMailClaimTimeout).After(now)
}
