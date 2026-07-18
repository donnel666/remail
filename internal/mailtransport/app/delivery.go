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
	FindByIdempotencyKey(ctx context.Context, idempotencyKey string) (*domain.OutboundMail, error)
	ListPending(ctx context.Context, limit int) ([]domain.OutboundMail, error)
	ActivateSending(ctx context.Context, idempotencyKey string, generation uint64, now time.Time) (bool, error)
	ReleasePending(ctx context.Context, idempotencyKey string, generation uint64, reason string) (bool, error)
	ResetPending(ctx context.Context, idempotencyKey string, generation uint64, reason string) (bool, error)
	RecordSendFailure(ctx context.Context, idempotencyKey string, generation uint64, reason string, retryable bool) (terminal bool, applied bool, err error)
	MarkSent(ctx context.Context, idempotencyKey string, generation uint64, now time.Time) (bool, error)
}

type SenderPort interface {
	Send(ctx context.Context, message domain.OutboundMessage) error
}

// OutboundSendFailure is an explicit remote business result. Unknown sender
// errors are infrastructure failures and must not consume the business budget.
type OutboundSendFailure struct {
	SafeMessage string
	Retryable   bool
	Cause       error
}

func (e *OutboundSendFailure) Error() string {
	if e == nil {
		return "outbound send failure"
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	if e.SafeMessage != "" {
		return e.SafeMessage
	}
	return "outbound send failure"
}

func (e *OutboundSendFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
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
