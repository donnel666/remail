package platform

import (
	"strings"

	"github.com/google/uuid"
)

// NewUUIDV7String returns a time-ordered UUID v7 string for backend-generated
// business identifiers. Do not use it for secrets such as API keys or tokens.
func NewUUIDV7String() string {
	id, err := uuid.NewV7()
	if err != nil {
		panic("uuid v7 entropy unavailable: " + err.Error())
	}
	return id.String()
}

// NewUUIDV4String returns an opaque random UUID v4 string for redeemable codes
// and other values that should not expose creation time.
func NewUUIDV4String() string {
	id, err := uuid.NewRandom()
	if err != nil {
		panic("uuid v4 entropy unavailable: " + err.Error())
	}
	return id.String()
}

// NewUUIDV7CompactString returns a UUID v7 without hyphens.
func NewUUIDV7CompactString() string {
	return strings.ReplaceAll(NewUUIDV7String(), "-", "")
}

// NewUUIDV7CompactUpper returns an uppercase UUID v7 without hyphens.
func NewUUIDV7CompactUpper() string {
	return strings.ToUpper(NewUUIDV7CompactString())
}

// NewUUIDV4CompactUpper returns an uppercase UUID v4 without hyphens.
func NewUUIDV4CompactUpper() string {
	return strings.ToUpper(strings.ReplaceAll(NewUUIDV4String(), "-", ""))
}
