package domain

import "time"

// UserLoginDevice tracks a user device fingerprint and its latest login time.
type UserLoginDevice struct {
	ID          uint64
	UserID      uint
	Fingerprint string
	LastLoginAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
