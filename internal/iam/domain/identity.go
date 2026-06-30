package domain

import "time"

// ThirdPartyIdentity links a user to an external identity provider account.
type ThirdPartyIdentity struct {
	ID             uint64
	UserID         uint
	Provider       string
	ProviderUserID string
	CreatedAt      time.Time
}
