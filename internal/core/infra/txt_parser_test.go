package infra

import (
	"errors"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/core/domain"
)

func TestTXTParser_ParseMicrosoftImport_ValidFormats(t *testing.T) {
	parser := NewTXTParser()
	content := `
user1@example.com----pass1
user2@example.com----pass2----aux@example.net
user3@example.com----pass3----client3----refresh3
user4@example.com----pass4----client4----refresh4----aux4@example.net
`

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategyAbort)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %+v", failures)
	}
	if len(result) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(result))
	}

	assertLine(t, result[0], "user1@example.com", "pass1", "", "", "")
	assertLine(t, result[1], "user2@example.com", "pass2", "", "", "aux@example.net")
	assertLine(t, result[2], "user3@example.com", "pass3", "client3", "refresh3", "")
	assertLine(t, result[3], "user4@example.com", "pass4", "client4", "refresh4", "aux4@example.net")
}

func TestTXTParser_ParseMicrosoftImport_EmptyReturnsError(t *testing.T) {
	parser := NewTXTParser()

	_, _, err := parser.ParseMicrosoftImport("", domain.ImportErrorStrategyAbort)
	if !errors.Is(err, domain.ErrInvalidImportFormat) {
		t.Errorf("expected ErrInvalidImportFormat, got %v", err)
	}
}

func TestTXTParser_ParseMicrosoftImport_Blanks(t *testing.T) {
	parser := NewTXTParser()
	content := "\n\nuser@example.com----pass123\n\n"

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategyAbort)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %+v", failures)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 line, got %d", len(result))
	}
	assertLine(t, result[0], "user@example.com", "pass123", "", "", "")
}

func TestTXTParser_ParseMicrosoftImport_PreservesPasswordWhitespace(t *testing.T) {
	parser := NewTXTParser()
	content := "user@example.com----  pass with spaces  \r\nuser2@example.com---- leading-and-trailing ----aux@example.net"

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategyAbort)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %+v", failures)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(result))
	}
	assertLine(t, result[0], "user@example.com", "  pass with spaces  ", "", "", "")
	assertLine(t, result[1], "user2@example.com", " leading-and-trailing ", "", "", "aux@example.net")
}

func TestTXTParser_ParseMicrosoftImport_AllCommentsReturnsError(t *testing.T) {
	parser := NewTXTParser()

	_, _, err := parser.ParseMicrosoftImport("# comment 1\n# comment 2\n\n", domain.ImportErrorStrategyAbort)
	if !errors.Is(err, domain.ErrInvalidImportFormat) {
		t.Errorf("expected ErrInvalidImportFormat, got %v", err)
	}
}

func TestTXTParser_ParseMicrosoftImport_InvalidLineReturnsError(t *testing.T) {
	parser := NewTXTParser()
	cases := []string{
		"justemail",
		"# comment",
		"----password",
		"email@example.com----",
		"email@example.com----password----",
		"email@example.com----password----client----",
		"email@example.com----password----client----refresh----",
		"email@example.com----password----client----refresh----aux----extra",
		"email@example.com:password",
	}

	for _, content := range cases {
		t.Run(content, func(t *testing.T) {
			_, _, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategyAbort)
			if !errors.Is(err, domain.ErrInvalidImportFormat) {
				t.Errorf("expected ErrInvalidImportFormat, got %v", err)
			}
		})
	}
}

func TestTXTParser_ParseMicrosoftImport_TrimsNonPasswordWhitespace(t *testing.T) {
	parser := NewTXTParser()
	content := "  user@example.com ---- pass123 ---- aux@example.net  \n\tuser2@test.com----pass456\n"

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategyAbort)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %+v", failures)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(result))
	}
	assertLine(t, result[0], "user@example.com", " pass123 ", "", "", "aux@example.net")
	assertLine(t, result[1], "user2@test.com", "pass456", "", "", "")
}

func TestTXTParser_ParseMicrosoftImport_SkipInvalidLines(t *testing.T) {
	parser := NewTXTParser()
	content := `
invalid-line
user@example.com----pass123
email@example.com----password----
user2@test.com----pass456
`

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategySkip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 valid lines, got %d", len(result))
	}
	assertLine(t, result[0], "user@example.com", "pass123", "", "", "")
	assertLine(t, result[1], "user2@test.com", "pass456", "", "", "")
	if len(failures) != 2 {
		t.Fatalf("expected 2 failures, got %+v", failures)
	}
	if failures[0].Line == 0 || failures[1].Line == 0 {
		t.Fatalf("expected failures to keep line numbers: %+v", failures)
	}
}

func TestTXTParser_ParseMicrosoftImport_SkipRejectsPseudoBindingFields(t *testing.T) {
	parser := NewTXTParser()
	content := strings.Join([]string{
		"valid1@example.com----pass1----client1----refresh1",
		"invalid-three@example.com----pass2----not-an-email",
		"invalid-five@example.com----password-prefix----password-suffix----00000000-0000-0000-0000-000000000000----" + strings.Repeat("r", 404),
		"valid2@example.com----pass3",
	}, "\n")

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategySkip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 valid lines, got %d", len(result))
	}
	assertLine(t, result[0], "valid1@example.com", "pass1", "client1", "refresh1", "")
	assertLine(t, result[1], "valid2@example.com", "pass3", "", "", "")
	if len(failures) != 2 {
		t.Fatalf("expected 2 failures, got %+v", failures)
	}
	if failures[0].Line != 2 || failures[1].Line != 3 {
		t.Fatalf("expected failures for lines 2 and 3, got %+v", failures)
	}
}

func TestTXTParser_ParseMicrosoftImport_AbortRejectsPseudoBindingField(t *testing.T) {
	parser := NewTXTParser()
	content := "invalid@example.com----password-prefix----password-suffix----00000000-0000-0000-0000-000000000000----" + strings.Repeat("r", 404)

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategyAbort)
	if !errors.Is(err, domain.ErrInvalidImportFormat) {
		t.Fatalf("expected ErrInvalidImportFormat, got %v", err)
	}
	if result != nil || failures != nil {
		t.Fatalf("expected abort to return no parsed result, got result=%+v failures=%+v", result, failures)
	}
}

func TestTXTParser_ParseMicrosoftImport_SkipRejectsValuesThatCannotFitPersistence(t *testing.T) {
	parser := NewTXTParser()
	content := strings.Join([]string{
		"invalid-email----password",
		"password-too-long@example.com----" + strings.Repeat("p", 513),
		"client-too-long@example.com----password----" + strings.Repeat("c", 256) + "----refresh",
		"refresh-too-long@example.com----password----client----" + strings.Repeat("r", 1025),
		"binding-too-long@example.com----password----" + strings.Repeat("a", 310) + "@example.com",
		"valid@example.com----password",
	}, "\n")

	result, failures, err := parser.ParseMicrosoftImport(content, domain.ImportErrorStrategySkip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 valid line, got %d", len(result))
	}
	assertLine(t, result[0], "valid@example.com", "password", "", "", "")
	if len(failures) != 5 {
		t.Fatalf("expected 5 failures, got %+v", failures)
	}
	for index, failure := range failures {
		if failure.Line != index+1 || failure.Category != "invalid_format" {
			t.Fatalf("unexpected failure at index %d: %+v", index, failure)
		}
	}
}

func assertLine(t *testing.T, line domain.MicrosoftImportLine, email, password, clientID, refreshToken, bindingAddress string) {
	t.Helper()
	if line.Email != email ||
		line.LineNumber == 0 ||
		line.Password != password ||
		line.ClientID != clientID ||
		line.RefreshToken != refreshToken ||
		line.BindingAddress != bindingAddress {
		t.Fatalf("line mismatch:\nwant email=%q password=%q clientID=%q refreshToken=%q bindingAddress=%q\ngot  %+v",
			email, password, clientID, refreshToken, bindingAddress, line)
	}
}
