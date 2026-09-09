package proton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/mail"
	"strings"
	"time"
)

type LoginRequest struct {
	Email    string
	Password string
	ProxyURL string
	PKL      []byte `json:"-"`
}

// Session contains secrets. Persist only through the private Proto session store.
type Session struct {
	Version        int           `json:"version"`
	UID            string        `json:"uid"`
	AccessToken    string        `json:"accessToken"`
	RefreshToken   string        `json:"refreshToken"`
	ExpiresAt      time.Time     `json:"expiresAt"`
	UserKeys       []string      `json:"userKeys"`
	Addresses      []AddressKeys `json:"addresses"`
	PKL            []byte        `json:"pkl,omitempty"`
	KeyFingerprint string        `json:"keyFingerprint,omitempty"`
}

const MaxPKLBytes = 8 << 20

// ValidFor checks the reusable metadata without decoding Python's trusted PKL.
// PKL is accepted only from the helper or the verified private session store.
func (s Session) ValidFor(email string) bool {
	if s.UID == "" {
		return false
	}
	switch s.Version {
	case 1:
		if s.AccessToken == "" || s.RefreshToken == "" {
			return false
		}
	case 2:
		if len(s.PKL) == 0 || len(s.PKL) > MaxPKLBytes || len(s.KeyFingerprint) != sha256.Size*2 || len(s.Addresses) == 0 || len(s.Addresses) > 4096 {
			return false
		}
		fingerprint, err := hex.DecodeString(s.KeyFingerprint)
		if err != nil || len(fingerprint) != sha256.Size {
			return false
		}
	default:
		return false
	}
	matched := false
	seen := make(map[string]bool)
	for _, address := range s.Addresses {
		if s.Version == 2 {
			parsed, err := mail.ParseAddress(address.Email)
			if address.ID == "" || seen[address.ID] || err != nil || parsed.Address != address.Email || len(address.PrivateKeys) != 0 {
				return false
			}
			seen[address.ID] = true
		}
		if address.ID != "" && strings.EqualFold(address.Email, email) {
			if s.Version == 2 {
				matched = true
			} else {
				for _, key := range address.PrivateKeys {
					matched = matched || strings.TrimSpace(key) != ""
				}
			}
		}
	}
	return matched
}

// SessionTokenIdentity is a comparison fence, never a value to log or expose.
func SessionTokenIdentity(session Session) string {
	if session.Version == 2 {
		if len(session.PKL) == 0 {
			return ""
		}
		digest := sha256.Sum256(session.PKL)
		return hex.EncodeToString(digest[:])
	}
	return session.RefreshToken
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
	Stage        string
	HTTPStatus   int
	APICode      int
	Category     string
	SafeMessage  string
	Retryable    bool
	ProxyFailure bool
	Cause        error
}

func (e *Failure) Error() string { return e.SafeMessage }
func (e *Failure) Unwrap() error { return e.Cause }
