package msacl

import (
	"os"
	"testing"
)

// TestMain injects the historical aishop6.com auxiliary/project domain for the
// whole package. Production sources the binding-domain list from
// domain_resources (purpose='binding') via SetAuxiliaryDomains; the tests use a
// fixed value instead of the (now defaultless) env fallback.
func TestMain(m *testing.M) {
	SetAuxiliaryDomains([]string{"aishop6.com"})
	os.Exit(m.Run())
}
