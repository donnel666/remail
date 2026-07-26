package infra

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/mailmatch/domain"
)

func TestTruncateDoesNotSplitUTF8(t *testing.T) {
	value := truncate(strings.Repeat("a", 999)+"中", 1000)
	if !utf8.ValidString(value) || len(value) > 1000 {
		t.Fatalf("truncate returned invalid UTF-8: %q", value)
	}
}

func TestMessageModelPreservesRawBodyExactly(t *testing.T) {
	body := " \n" + strings.Repeat("a", 70*1024) + "\n "
	model := messageModelFromDomain(domain.Message{RawBody: body})
	if !model.RawBody.Valid || model.RawBody.String != body {
		t.Fatalf("raw body changed before storage: valid=%v bytes=%d", model.RawBody.Valid, len(model.RawBody.String))
	}
}
