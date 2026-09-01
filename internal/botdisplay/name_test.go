package botdisplay

import "testing"

func TestNameMatchesWebLeaderboardDisplay(t *testing.T) {
	if got := Name(" Alice ", "alice@example.com", 42); got != "Alice" {
		t.Fatalf("nickname = %q", got)
	}
	if got := Name("", "secret@example.com", 42); got != "secret" {
		t.Fatalf("email prefix = %q", got)
	}
	if got := Name("42", "owner@example.com", 42); got != "42" {
		t.Fatalf("numeric nickname = %q", got)
	}
	if got := Name("line\nbreak", "owner@example.com", 42); got != "owner" {
		t.Fatalf("control nickname fallback = %q", got)
	}
	if got := Name("", "legacy", 42); got != "legacy" {
		t.Fatalf("legacy email fallback = %q", got)
	}
	if got := Name("", "", 42); got != "#42" {
		t.Fatalf("legacy fallback = %q", got)
	}
}
