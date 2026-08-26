package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEmailSetAndWriteCandidates(t *testing.T) {
	dir := t.TempDir()
	luckmail := filepath.Join(dir, "luckmail.txt")
	if err := os.WriteFile(luckmail, []byte("A@example.com----ignored\nsecond@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := loadEmailSet(luckmail)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set["a@example.com"]; !ok {
		t.Fatalf("normalized imported email missing: %#v", set)
	}
	if _, ok := set["second@example.com"]; !ok {
		t.Fatalf("plain email missing: %#v", set)
	}
	input := filepath.Join(dir, "selected.txt")
	if err := writeCandidates(input, []candidate{{id: 1, email: "A@example.com"}}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "A@example.com\n"; got != want {
		t.Fatalf("selected input = %q, want %q", got, want)
	}
}

func TestActivePoolPointerRoundTrip(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pools", "pending-1")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeActivePoolPointer(root, dir); err != nil {
		t.Fatal(err)
	}
	cfg := config{stateDir: root, inputFile: filepath.Join(root, "pending-validation-input.txt")}
	got, err := loadActivePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.input != filepath.Join(dir, "pending-validation-input.txt") || got.state != filepath.Join(dir, "pending-validation.json") {
		t.Fatalf("active pool paths = %#v", got)
	}
}

func TestValidationCheckpointDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"phase":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	done, err := validationCheckpointDone(path)
	if err != nil || !done {
		t.Fatalf("validationCheckpointDone() = %v, %v", done, err)
	}
}

func TestWaitForPoolLowWater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"phase":"processing","total":1000,"freezeOffset":500}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForPoolLowWater(context.Background(), path, 500); err != nil {
		t.Fatal(err)
	}
}
