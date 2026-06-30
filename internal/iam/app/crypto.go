package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
)

// newCryptoID generates a cryptographically random hex string.
func newCryptoID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func generateRandomDigits(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("invalid digit length %d", length)
	}

	digits := make([]byte, length)
	for i := range digits {
		value, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", fmt.Errorf("crypto/rand: %w", err)
		}
		digits[i] = byte('0' + value.Int64())
	}
	return string(digits), nil
}
