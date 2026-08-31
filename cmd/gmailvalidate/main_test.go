package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsAppPasswordOnlyInputBeforeSuggestingApply(t *testing.T) {
	input := filepath.Join(t.TempDir(), "gmail.txt")
	if err := os.WriteFile(input, []byte("owner@gmail.com----abcd efgh ijkl mnop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{"-file", input, "-line", "1"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "requires the Gmail login password") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout.String(), "add -apply") {
		t.Fatalf("APP-password-only input was incorrectly advertised as applicable: %s", stdout.String())
	}
}

func TestCredentialSelectionCheckpointAndSuccessReplacement(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "gmail.txt")
	if err := os.WriteFile(input, []byte("first@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop\nsecond@gmail.com----password----backup@example.com----ponmlkjihgfedcba\n"), 0o600); err != nil {
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
	revoked := loaded.Accounts[credential.Email]
	revoked.TwoFactorSecret = ""
	revoked.TwoFactorRevoked = true
	loaded.Accounts[credential.Email] = revoked
	if err := saveCheckpoint(statePath, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadCheckpoint(statePath)
	if err != nil || !loaded.Accounts[credential.Email].TwoFactorRevoked {
		t.Fatal("revoked 2FA checkpoint was lost")
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
