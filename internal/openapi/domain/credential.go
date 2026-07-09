package domain

import (
	"errors"
	"strings"
	"time"
)

type APIKey struct {
	ID                 uint
	UserID             uint
	OwnerRole          string
	Name               string
	KeyPrefix          string
	KeyPlain           string
	Enabled            bool
	RateLimitPerMinute *int
	ConcurrencyLimit   int
	QuotaLimit         *int64
	QuotaUsed          int64
	ActiveRequests     int
	ExpireAt           *time.Time
	LastUsedAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type OrderToken struct {
	ID             uint
	TokenPrefix    string
	TokenPlain     string
	OrderNo        string
	Enabled        bool
	ExpireAt       *time.Time
	DisabledAt     *time.Time
	DisabledReason string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

var (
	ErrInvalidAPIKey           = errors.New("openapi: invalid api key")
	ErrAPIKeyNotFound          = errors.New("openapi: api key not found")
	ErrAPIKeyDisabled          = errors.New("openapi: api key disabled")
	ErrAPIKeyExpired           = errors.New("openapi: api key expired")
	ErrAPIKeyForbidden         = errors.New("openapi: api key forbidden")
	ErrAPIKeyRateLimited       = errors.New("openapi: api key rate limited")
	ErrAPIKeyQuotaExceeded     = errors.New("openapi: api key quota exceeded")
	ErrAPIKeyConcurrencyLimit  = errors.New("openapi: api key concurrency limit reached")
	ErrInvalidOrderToken       = errors.New("openapi: invalid order token")
	ErrOrderTokenDisabled      = errors.New("openapi: order token disabled")
	ErrOrderTokenExpired       = errors.New("openapi: order token expired")
	ErrIdempotencyRequired     = errors.New("openapi: idempotency key required")
	ErrIdempotencyConflict     = errors.New("openapi: idempotency conflict")
	ErrInvalidCredentialFilter = errors.New("openapi: invalid credential filter")
)

func NormalizeAPIKeyName(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= 120 {
		return trimmed
	}
	return trimmed[:120]
}
