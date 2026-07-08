package api

import "time"

type KeyCreateRequest struct {
	Name               string     `json:"name"`
	ExpireAt           *time.Time `json:"expireAt"`
	RateLimitPerMinute int        `json:"rateLimitPerMinute"`
	ConcurrencyLimit   int        `json:"concurrencyLimit"`
}

type KeyPatchRequest struct {
	Name               *string    `json:"name"`
	Enabled            *bool      `json:"enabled"`
	ExpireAt           *time.Time `json:"expireAt"`
	ExpireSet          bool       `json:"-"`
	RateLimitPerMinute *int       `json:"rateLimitPerMinute"`
	ConcurrencyLimit   *int       `json:"concurrencyLimit"`
}

type KeyResponse struct {
	ID                 uint       `json:"id"`
	Name               string     `json:"name"`
	KeyPrefix          string     `json:"keyPrefix"`
	KeyPlain           string     `json:"keyPlain,omitempty"`
	Enabled            bool       `json:"enabled"`
	RateLimitPerMinute int        `json:"rateLimitPerMinute"`
	ConcurrencyLimit   int        `json:"concurrencyLimit"`
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
