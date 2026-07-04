package app

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
)

const (
	outboundMailClaimTimeout = 2 * time.Minute
)

type OutboundMailStore interface {
	Reserve(ctx context.Context, mail *domain.OutboundMail) (*domain.OutboundMail, bool, error)
	ClaimSending(ctx context.Context, idempotencyKey string, staleBefore time.Time, now time.Time) (*domain.OutboundMail, bool, error)
	ClaimDispatchable(ctx context.Context, limit int, staleBefore time.Time) ([]domain.OutboundMail, error)
	MarkPending(ctx context.Context, idempotencyKey string, reason string) error
	MarkSent(ctx context.Context, idempotencyKey string, now time.Time) error
	MarkFailed(ctx context.Context, idempotencyKey string, reason string) error
}

type SenderPort interface {
	Send(ctx context.Context, message domain.OutboundMessage) error
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
