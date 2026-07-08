package domain

import "time"

// Session represents an authenticated user session.
// Stored in Redis and referenced by an HttpOnly cookie.
type Session struct {
	ID           string
	UserID       uint
	Role         Role
	Email        string
	TokenVersion int
	CreatedAt    time.Time
}
