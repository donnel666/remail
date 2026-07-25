package infra

import (
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"golang.org/x/crypto/bcrypt"
)

func TestHasher_HashAndVerify(t *testing.T) {
	runtimeconfig.Set("bcrypt_cost", "4")
	t.Cleanup(func() { runtimeconfig.Delete("bcrypt_cost") })
	h := NewHasher()

	password := "TestPassword123!"
	hash, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if hash == "" {
		t.Fatal("Hash() returned empty string")
	}
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != 4 {
		t.Fatalf("bcrypt cost = %d, %v; want 4", cost, err)
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
