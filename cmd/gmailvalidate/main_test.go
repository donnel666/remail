package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialSelectionCheckpointAndSuccessReplacement(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "gmail.txt")
	if err := os.WriteFile(input, []byte("first@gmail.com----password----JBSWY3DPEHPK3PXP\nsecond@gmail.com----password----backup@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, err := loadCredential(input, 2)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Email != "second@gmail.com" || credential.BindingEmail != "backup@example.com" || credential.TwoFactorSecret != "" {
		t.Fatalf("unexpected selected credential metadata")
	}

	statePath := filepath.Join(dir, "state.json")
	state := checkpointFile{Version: 1, Accounts: map[string]accountCheckpoint{
		credential.Email: {TwoFactorSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"},
	}}
	if err := saveCheckpoint(statePath, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadCheckpoint(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Accounts[credential.Email].TwoFactorSecret == "" {
		t.Fatal("authoritative 2FA checkpoint was lost")
	}
	info, err := os.Stat(statePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode = %v, err = %v", info.Mode().Perm(), err)
	}

	output := filepath.Join(dir, "gmail_ok.txt")
	if err := upsertSuccess(output, credential.Email, "second@gmail.com----password----secret-one----app-one"); err != nil {
		t.Fatal(err)
	}
	if err := upsertSuccess(output, credential.Email, "second@gmail.com----password----secret-two----app-two"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second@gmail.com----password----secret-two----app-two\n" {
		t.Fatal("successful credential output was not replaced atomically")
	}
}
