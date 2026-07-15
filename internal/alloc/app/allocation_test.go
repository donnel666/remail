package app

import (
	"strings"
	"testing"
)

func TestGeneratedMailboxVariantsUseHumanNamesAndUpToSixDigits(t *testing.T) {
	if len(biblicalMailboxNames) < 1_000 {
		t.Fatalf("got %d biblical names, want at least 1000", len(biblicalMailboxNames))
	}
	names := make(map[string]struct{}, generatedMailboxNameCount())
	for i := 0; i < generatedMailboxNameCount(); i++ {
		name := generatedMailboxName(i)
		if strings.Contains(name, ".") {
			t.Fatalf("generated mailbox base name contains a dot: %q", name)
		}
		names[name] = struct{}{}
	}
	if len(names) < 10_000 {
		t.Fatalf("got %d unique base names, want at least 10000", len(names))
	}
	variants := generatedMailboxVariants("Example.COM")
	if len(variants) != aliasGenerationWindow {
		t.Fatalf("got %d variants, want %d", len(variants), aliasGenerationWindow)
	}
	for _, email := range variants {
		local, domain, ok := splitEmail(email)
		name := strings.TrimRight(local, "0123456789")
		digits := strings.TrimPrefix(local, name)
		if _, known := names[name]; !ok || !known || strings.Contains(name, ".") || domain != "example.com" || len(digits) > 6 {
			t.Fatalf("unexpected generated mailbox %q", email)
		}
	}
}
