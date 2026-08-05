package mailbox

import "testing"

func TestAddressNormalizesMailboxAliases(t *testing.T) {
	tests := map[string]string{
		"User.Name+tag@Example.COM": "username@example.com",
		"USER.NAME@example.com":     "username@example.com",
		"username@example.com":      "username@example.com",
	}
	for input, want := range tests {
		if got := Normalize(input); got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAddressRejectsInvalidMailboxKeys(t *testing.T) {
	for _, input := range []string{"", "@example.com", "+tag@example.com", "...@example.com", "user"} {
		if exact, key, ok := Address(input); ok || exact != "" || key != "" {
			t.Fatalf("Address(%q) = (%q, %q, %v), want invalid", input, exact, key, ok)
		}
	}
}
