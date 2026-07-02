package infra

import (
	"strings"

	"github.com/donnel666/remail/internal/core/domain"
)

// TXTParser parses resource import TXT files.
type TXTParser struct{}

// NewTXTParser creates a new TXTParser.
func NewTXTParser() *TXTParser {
	return &TXTParser{}
}

// ParseMicrosoftImport parses Microsoft TXT import content.
// Format: one resource per line, separated by "----":
//
//	email----password
//	email----password----bindingAddress
//	email----password----clientId----refreshToken
//	email----password----clientId----refreshToken----bindingAddress
//
// Empty lines are skipped.
func (p *TXTParser) ParseMicrosoftImport(content string, strategy domain.ImportErrorStrategy) ([]domain.MicrosoftImportLine, []domain.ImportLineError, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil, domain.ErrInvalidImportFormat
	}
	if strategy == "" {
		strategy = domain.ImportErrorStrategySkip
	}

	lines := strings.Split(content, "\n")
	var result []domain.MicrosoftImportLine
	var failures []domain.ImportLineError
	for i, line := range lines {
		lineNumber := i + 1
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parsed, lineErr := parseMicrosoftImportLine(lineNumber, line)
		if lineErr != nil {
			if strategy == domain.ImportErrorStrategyAbort {
				return nil, nil, lineErr
			}
			failures = append(failures, *lineErr)
			continue
		}

		result = append(result, *parsed)
	}

	if len(result) == 0 && len(failures) == 0 {
		return nil, nil, domain.ErrInvalidImportFormat
	}

	return result, failures, nil
}

func parseMicrosoftImportLine(lineNumber int, line string) (*domain.MicrosoftImportLine, *domain.ImportLineError) {
	parts := strings.Split(line, "----")
	if len(parts) != 2 && len(parts) != 3 && len(parts) != 4 && len(parts) != 5 {
		return nil, importLineError(lineNumber, parts)
	}

	email := strings.TrimSpace(parts[0])
	password := strings.TrimSpace(parts[1])
	if email == "" || password == "" {
		return nil, importLineError(lineNumber, parts)
	}

	clientID := ""
	refreshToken := ""
	bindingAddress := ""
	switch len(parts) {
	case 3:
		bindingAddress = strings.TrimSpace(parts[2])
		if bindingAddress == "" {
			return nil, importLineError(lineNumber, parts)
		}
	case 4:
		clientID = strings.TrimSpace(parts[2])
		refreshToken = strings.TrimSpace(parts[3])
		if clientID == "" || refreshToken == "" {
			return nil, importLineError(lineNumber, parts)
		}
	case 5:
		clientID = strings.TrimSpace(parts[2])
		refreshToken = strings.TrimSpace(parts[3])
		bindingAddress = strings.TrimSpace(parts[4])
		if clientID == "" || refreshToken == "" || bindingAddress == "" {
			return nil, importLineError(lineNumber, parts)
		}
	}

	return &domain.MicrosoftImportLine{
		LineNumber:     lineNumber,
		Email:          email,
		Password:       password,
		ClientID:       clientID,
		RefreshToken:   refreshToken,
		BindingAddress: bindingAddress,
	}, nil
}

func importLineError(lineNumber int, parts []string) *domain.ImportLineError {
	email := ""
	if len(parts) > 0 {
		email = strings.TrimSpace(parts[0])
	}
	return &domain.ImportLineError{
		Line:        lineNumber,
		Email:       email,
		Category:    "invalid_format",
		SafeMessage: "Invalid import format.",
	}
}
