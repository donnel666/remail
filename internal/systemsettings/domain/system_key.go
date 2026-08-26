package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalidSystemKey  = errors.New("invalid system key")
	ErrSystemKeyNotFound = errors.New("system key not found")
)

type SystemKeyPurpose string

const (
	SystemKeyPurposeICloudForwarding SystemKeyPurpose = "icloud_forwarding"
	SystemKeyPurposeSMTPSubmission   SystemKeyPurpose = "smtp_submission"
)

type SystemKey struct {
	ID         uint
	Name       string
	Purpose    SystemKeyPurpose
	KeyPrefix  string
	KeyPlain   string
	LastUsedAt *time.Time
	CreatedAt  time.Time
}
