package infra

import (
	"github.com/donnel666/remail/internal/proto/domain"
	"testing"
)

func TestParseImportStrictTwoFields(t *testing.T) {
	rows, failures, err := ParseImport("a@example.com----p@ss\nb@example.com----x----extra\n", domain.ErrorStrategySkip)
	if err != nil || len(rows) != 1 || len(failures) != 1 {
		t.Fatalf("rows=%d failures=%d err=%v", len(rows), len(failures), err)
	}
	if rows[0].Password != "p@ss" {
		t.Fatal("password changed")
	}
}

func TestParseImportAbort(t *testing.T) {
	if _, _, err := ParseImport("a@example.com----x----y", domain.ErrorStrategyAbort); err == nil {
		t.Fatal("expected strict format error")
	}
}

func TestParseImportPreservesPasswordWhitespace(t *testing.T) {
	rows, failures, err := ParseImport("a@example.com----  pass with spaces  \r\n", domain.ErrorStrategyAbort)
	if err != nil || len(failures) != 0 || len(rows) != 1 {
		t.Fatalf("rows=%d failures=%d err=%v", len(rows), len(failures), err)
	}
	if rows[0].Password != "  pass with spaces  " {
		t.Fatalf("password whitespace changed: %q", rows[0].Password)
	}
}
