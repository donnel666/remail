package msacl

import (
	"strings"
	"testing"
)

func TestNextAuxiliaryDomainRoundRobin(t *testing.T) {
	defer SetAuxiliaryDomains([]string{"aishop6.com"}) // restore TestMain default

	// Empty list must error, never fabricate a default.
	SetAuxiliaryDomains(nil)
	if _, err := nextAuxiliaryDomain(); err == nil {
		t.Fatal("expected error on empty binding-domain list")
	}

	// Single domain always returned.
	SetAuxiliaryDomains([]string{"aishop6.com"})
	for i := 0; i < 3; i++ {
		if d, err := nextAuxiliaryDomain(); err != nil || d != "aishop6.com" {
			t.Fatalf("single: got %q err=%v", d, err)
		}
	}

	// Round-robin over 2 domains: 4 consecutive picks yield 2 of each,
	// independent of the starting cursor offset.
	SetAuxiliaryDomains([]string{"aishop6.com", "lclaitech.com"})
	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		d, err := nextAuxiliaryDomain()
		if err != nil {
			t.Fatalf("multi err=%v", err)
		}
		counts[d]++
	}
	if counts["aishop6.com"] != 2 || counts["lclaitech.com"] != 2 {
		t.Fatalf("round-robin uneven: %v", counts)
	}
}

func TestSetAuxiliaryDomainsNormalizes(t *testing.T) {
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{" AIShop6.com ", "aishop6.com", ".lclaitech.com.", ""})
	got := activeAuxiliaryDomains()
	want := []string{"aishop6.com", "lclaitech.com"}
	if len(got) != len(want) {
		t.Fatalf("normalize got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalize got %v want %v", got, want)
		}
	}
}

func TestMapExplicitAliasErrorAlreadyBoundShowsMaskedAddress(t *testing.T) {
	res := mapExplicitAliasError(&AuthError{Status: AuthStatusAlreadyBound, BoundMailbox: "a****b@qq.com"})
	if res.Category != "already_bound" {
		t.Fatalf("category = %q", res.Category)
	}
	if !strings.Contains(res.SafeMessage, "a****b@qq.com") {
		t.Fatalf("SafeMessage missing masked address: %q", res.SafeMessage)
	}
}

func TestDomainInProjectUsesInjectedList(t *testing.T) {
	defer SetAuxiliaryDomains([]string{"aishop6.com"})
	SetAuxiliaryDomains([]string{"aishop6.com", "lclaitech.com"})
	if !domainInProject("lclaitech.com") {
		t.Fatal("lclaitech.com should be recognized as a binding domain")
	}
	if domainInProject("qq.com") {
		t.Fatal("qq.com must not be recognized as a binding domain")
	}
}
