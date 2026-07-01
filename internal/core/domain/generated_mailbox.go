package domain

import "time"

// GeneratedMailboxStatus represents the status of a generated mailbox.
type GeneratedMailboxStatus string

const (
	GeneratedMailboxNormal   GeneratedMailboxStatus = "normal"
	GeneratedMailboxDisabled GeneratedMailboxStatus = "disabled"
)

// GeneratedMailbox represents a mailbox generated on a domain resource.
type GeneratedMailbox struct {
	ID              uint
	ResourceID      uint
	Email           string
	Status          GeneratedMailboxStatus
	LastAllocatedAt *time.Time
	CreatedAt       time.Time
}

// IsValidGeneratedMailboxStatus returns true if the status is valid.
func IsValidGeneratedMailboxStatus(s string) bool {
	switch GeneratedMailboxStatus(s) {
	case GeneratedMailboxNormal, GeneratedMailboxDisabled:
		return true
	default:
		return false
	}
}
