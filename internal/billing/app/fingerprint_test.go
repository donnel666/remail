package app

import "testing"

func TestFingerprintUsesPointsNamespace(t *testing.T) {
	const want = "207671261f7c3ea44fbbc1445d04d8f82d20125c956263310898769a5ad58378"
	if got := fingerprint("wallet.adjust", 1, "10"); got != want {
		t.Fatalf("fingerprint() = %q, want %q", got, want)
	}
}
