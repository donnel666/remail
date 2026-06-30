package infra

import (
	"testing"
)

func TestHasher_HashAndVerify(t *testing.T) {
	h := NewHasher()

	password := "TestPassword123!"
	hash, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if hash == "" {
		t.Fatal("Hash() returned empty string")
	}

	// Verify correct password
	if !h.Verify(password, hash) {
		t.Error("Verify() returned false for correct password")
	}

	// Verify wrong password
	if h.Verify("wrongpassword", hash) {
		t.Error("Verify() returned true for wrong password")
	}

	// Verify empty password
	if h.Verify("", hash) {
		t.Error("Verify() returned true for empty password")
	}
}

func TestHasher_DifferentPasswordsDifferentHashes(t *testing.T) {
	h := NewHasher()

	hash1, _ := h.Hash("password1")
	hash2, _ := h.Hash("password2")

	if hash1 == hash2 {
		t.Error("different passwords should produce different hashes")
	}
}
