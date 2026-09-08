package proton

import (
	"context"
	"time"
)

type LoginRequest struct {
	Email    string
	Password string
	ProxyURL string
}

// Session contains secrets. Persist only through the encrypted Proto session store.
type Session struct {
	Version      int           `json:"version"`
	UID          string        `json:"uid"`
	AccessToken  string        `json:"accessToken"`
	RefreshToken string        `json:"refreshToken"`
	ExpiresAt    time.Time     `json:"expiresAt"`
	UserKeys     []string      `json:"userKeys"`
	Addresses    []AddressKeys `json:"addresses"`
}

type AddressKeys struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	PrivateKeys []string `json:"privateKeys"`
}

type FetchRequest struct {
	ProxyURL        string
	Recipient       string
	SinceAt         time.Time
	UntilAt         time.Time
	MaxMessages     int
	FullHistory     bool
	KnownMessageIDs []string
	OnSession       func(Session) error
	RefreshSession  func(context.Context, *Session, func(context.Context, *Session) error) error
	OnMessages      func([]Message) error
}

type FetchResult struct {
	Messages []Message
	Complete bool
}

type Address struct {
	Name    string
	Address string
}

type Message struct {
	ID              string
	AddressID       string
	Subject         string
	Body            string
	MIMEType        string
	Header          string
	ExternalID      string
	Folder          string
	Sender          Address
	ToList          []Address
	CCList          []Address
	BCCList         []Address
	OriginalToCount int
	ReceivedAt      time.Time
}

type Failure struct {
	Category     string
	SafeMessage  string
	Retryable    bool
	ProxyFailure bool
	Cause        error
}

func (e *Failure) Error() string { return e.SafeMessage }
func (e *Failure) Unwrap() error { return e.Cause }
