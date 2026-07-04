package domain

import (
	"strings"
	"time"
)

type MicrosoftBindingStatus string

const (
	MicrosoftBindingPending  MicrosoftBindingStatus = "pending"
	MicrosoftBindingCodeSent MicrosoftBindingStatus = "code_sent"
	MicrosoftBindingVerified MicrosoftBindingStatus = "verified"
	MicrosoftBindingTimeout  MicrosoftBindingStatus = "timeout"
	MicrosoftBindingFailed   MicrosoftBindingStatus = "failed"
	MicrosoftBindingExpired  MicrosoftBindingStatus = "expired"
)

type MicrosoftBindingMailbox struct {
	ID             uint
	ResourceID     uint
	OwnerUserID    uint
	AccountEmail   string
	BindingAddress string
	Purpose        string
	Status         MicrosoftBindingStatus
	CodeMessageID  string
	BoundDisplay   string
	Category       string
	LastSafeError  string
	SelectedAt     *time.Time
	CodeSentAt     *time.Time
	VerifiedAt     *time.Time
	ExpiresAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func NewMicrosoftBindingMailbox(resourceID uint, ownerUserID uint, accountEmail string, bindingAddress string, now time.Time) *MicrosoftBindingMailbox {
	return &MicrosoftBindingMailbox{
		ResourceID:     resourceID,
		OwnerUserID:    ownerUserID,
		AccountEmail:   normalizeMailbox(accountEmail),
		BindingAddress: normalizeMailbox(bindingAddress),
		Purpose:        "validation",
		Status:         MicrosoftBindingPending,
		SelectedAt:     &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func (m *MicrosoftBindingMailbox) MarkCodeSent(now time.Time) {
	m.Status = MicrosoftBindingCodeSent
	m.LastSafeError = ""
	m.CodeSentAt = &now
	m.UpdatedAt = now
}

func (m *MicrosoftBindingMailbox) MarkVerified(now time.Time) {
	m.Status = MicrosoftBindingVerified
	m.Category = ""
	m.LastSafeError = ""
	m.VerifiedAt = &now
	m.UpdatedAt = now
}

func (m *MicrosoftBindingMailbox) MarkFailed(status MicrosoftBindingStatus, safeError string, now time.Time) {
	switch status {
	case MicrosoftBindingTimeout, MicrosoftBindingFailed, MicrosoftBindingExpired:
		m.Status = status
	default:
		m.Status = MicrosoftBindingFailed
	}
	m.LastSafeError = strings.TrimSpace(safeError)
	m.Category = string(status)
	m.UpdatedAt = now
}

func normalizeMailbox(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
