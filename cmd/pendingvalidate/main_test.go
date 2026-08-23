package main

import (
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
