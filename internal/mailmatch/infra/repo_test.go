package infra

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateDoesNotSplitUTF8(t *testing.T) {
	value := truncate(strings.Repeat("a", 999)+"中", 1000)
	if !utf8.ValidString(value) || len(value) > 1000 {
		t.Fatalf("truncate returned invalid UTF-8: %q", value)
	}
}
