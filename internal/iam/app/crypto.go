package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
)

// newCryptoID generates a cryptographically random hex string.
func newCryptoID() (string, error) {
	b, err := newCryptoBytes(24)
	if err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func newCryptoBytes(length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("invalid random byte length %d", length)
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
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
