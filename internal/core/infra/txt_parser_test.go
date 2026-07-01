package infra

import (
	"errors"
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

	result, err := parser.ParseMicrosoftImport(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

	_, err := parser.ParseMicrosoftImport("")
	if !errors.Is(err, domain.ErrInvalidImportFormat) {
		t.Errorf("expected ErrInvalidImportFormat, got %v", err)
	}
}

func TestTXTParser_ParseMicrosoftImport_Blanks(t *testing.T) {
	parser := NewTXTParser()
	content := "\n\nuser@example.com----pass123\n\n"

	result, err := parser.ParseMicrosoftImport(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 line, got %d", len(result))
	}
	assertLine(t, result[0], "user@example.com", "pass123", "", "", "")
}

func TestTXTParser_ParseMicrosoftImport_AllCommentsReturnsError(t *testing.T) {
	parser := NewTXTParser()

	_, err := parser.ParseMicrosoftImport("# comment 1\n# comment 2\n\n")
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
			_, err := parser.ParseMicrosoftImport(content)
			if !errors.Is(err, domain.ErrInvalidImportFormat) {
				t.Errorf("expected ErrInvalidImportFormat, got %v", err)
			}
		})
	}
}

func TestTXTParser_ParseMicrosoftImport_TrimsWhitespace(t *testing.T) {
	parser := NewTXTParser()
	content := "  user@example.com ---- pass123 ---- aux@example.net  \n\tuser2@test.com----pass456\n"

	result, err := parser.ParseMicrosoftImport(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(result))
	}
	assertLine(t, result[0], "user@example.com", "pass123", "", "", "aux@example.net")
	assertLine(t, result[1], "user2@test.com", "pass456", "", "", "")
}

func assertLine(t *testing.T, line domain.MicrosoftImportLine, email, password, clientID, refreshToken, auxiliaryAddress string) {
	t.Helper()
	if line.Email != email ||
		line.LineNumber == 0 ||
		line.Password != password ||
		line.ClientID != clientID ||
		line.RefreshToken != refreshToken ||
		line.AuxiliaryAddress != auxiliaryAddress {
		t.Fatalf("line mismatch:\nwant email=%q password=%q clientID=%q refreshToken=%q auxiliaryAddress=%q\ngot  %+v",
			email, password, clientID, refreshToken, auxiliaryAddress, line)
	}
}
