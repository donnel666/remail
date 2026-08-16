package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalidSystemKey  = errors.New("invalid system key")
	ErrSystemKeyNotFound = errors.New("system key not found")
)

type SystemKey struct {
	ID         uint
	Name       string
	KeyPrefix  string
	KeyPlain   string
	LastUsedAt *time.Time
	CreatedAt  time.Time
}
