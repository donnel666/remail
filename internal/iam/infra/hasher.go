package infra

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// Hasher implements app.Hasher using bcrypt.
type Hasher struct{}

// NewHasher creates a new bcrypt hasher.
func NewHasher() *Hasher {
	return &Hasher{}
}

// Hash returns a bcrypt hash of the password.
func (h *Hasher) Hash(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt hash: %w", err)
	}
	return string(bytes), nil
}

// Verify compares a password against a bcrypt hash.
func (h *Hasher) Verify(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
