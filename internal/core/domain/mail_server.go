package domain

import "time"

// MailServerStatus represents the operational status of a mail server.
type MailServerStatus string

const (
	MailServerOnline   MailServerStatus = "online"
	MailServerOffline  MailServerStatus = "offline"
	MailServerDisabled MailServerStatus = "disabled"
)

// MailServer represents a self-hosted or third-party mail server.
type MailServer struct {
	ID            uint
	OwnerUserID   uint
	Name          string
	ServerAddress string
	MXRecord      string
	SPFRecord     string
	DKIMRecord    string
	DMARCRecord   string
	PTRRecord     string
	Status        MailServerStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// IsValidMailServerStatus returns true if the status is a valid state.
func IsValidMailServerStatus(s string) bool {
	switch MailServerStatus(s) {
	case MailServerOnline, MailServerOffline, MailServerDisabled:
		return true
	default:
		return false
	}
}
