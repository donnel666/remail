package domain

import "errors"

var (
	ErrProxyNotFound        = errors.New("proxy not found")
	ErrInvalidProxyPool     = errors.New("invalid proxy pool")
	ErrInvalidProxyURL      = errors.New("invalid proxy url")
	ErrInvalidProxyStatus   = errors.New("invalid proxy status")
	ErrInvalidProxyExpireAt = errors.New("invalid proxy expiration time")
	ErrInvalidProxyFilter   = errors.New("invalid proxy filter")
	ErrDuplicateProxy       = errors.New("duplicate proxy")
	ErrProxyCheckFailed     = errors.New("proxy check failed")
	ErrProxyUnavailable     = errors.New("proxy unavailable")
	ErrProxyBindingInvalid  = errors.New("invalid proxy binding")
)
