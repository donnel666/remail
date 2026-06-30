package domain

import "time"

// Invite is an administrator-managed registration invitation.
type Invite struct {
	Code      string
	Enabled   bool
	MaxUse    int
	Used      int
	ExpireAt  *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// InviteUse is the immutable fact that an invitation was consumed by a user.
type InviteUse struct {
	ID         uint64
	InviteCode string
	UserID     uint
	UsedAt     time.Time
}
