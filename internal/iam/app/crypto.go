package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newCryptoID generates a cryptographically random hex string.
func newCryptoID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
