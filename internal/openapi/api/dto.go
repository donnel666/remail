package api

import "time"

type KeyCreateRequest struct {
	Name               string     `json:"name"`
	ExpireAt           *time.Time `json:"expireAt"`
	RateLimitPerMinute *int       `json:"rateLimitPerMinute"`
	ConcurrencyLimit   int        `json:"concurrencyLimit"`
	QuotaLimit         *int64     `json:"quotaLimit"`
}

type KeyPatchRequest struct {
	Name               *string    `json:"name"`
	Enabled            *bool      `json:"enabled"`
	ExpireAt           *time.Time `json:"expireAt"`
	ExpireSet          bool       `json:"-"`
	RateLimitPerMinute *int       `json:"rateLimitPerMinute"`
	RateLimitSet       bool       `json:"-"`
	ConcurrencyLimit   *int       `json:"concurrencyLimit"`
	QuotaLimit         *int64     `json:"quotaLimit"`
	QuotaSet           bool       `json:"-"`
}

type KeyResponse struct {
	ID                 uint       `json:"id"`
	Name               string     `json:"name"`
	KeyPrefix          string     `json:"keyPrefix"`
	KeyPlain           string     `json:"keyPlain,omitempty"`
	Enabled            bool       `json:"enabled"`
	RateLimitPerMinute *int       `json:"rateLimitPerMinute"`
	ConcurrencyLimit   int        `json:"concurrencyLimit"`
	QuotaLimit         *int64     `json:"quotaLimit,omitempty"`
	QuotaUsed          int64      `json:"quotaUsed"`
	RemainingQuota     *int64     `json:"remainingQuota,omitempty"`
	ActiveRequests     int        `json:"activeRequests"`
	ExpireAt           *time.Time `json:"expireAt,omitempty"`
	LastUsedAt         *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type KeyListResponse struct {
	Items  []KeyResponse `json:"items"`
	Total  int64         `json:"total"`
	Offset int           `json:"offset"`
	Limit  int           `json:"limit"`
}

type KeyUsageResponse struct {
	RequestCount int64 `json:"requestCount"`
	KeyCount     int64 `json:"keyCount"`
}

type KeyProfileResponse struct {
	APIKey KeyResponse `json:"apiKey"`
}
