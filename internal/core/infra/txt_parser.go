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
//	email----password----auxiliaryAddress
//	email----password----clientId----refreshToken
//	email----password----clientId----refreshToken----auxiliaryAddress
//
// Empty lines are skipped.
func (p *TXTParser) ParseMicrosoftImport(content string) ([]domain.MicrosoftImportLine, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, domain.ErrInvalidImportFormat
	}

	lines := strings.Split(content, "\n")
	var result []domain.MicrosoftImportLine
	for i, line := range lines {
		lineNumber := i + 1
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

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
		auxiliaryAddress := ""
		switch len(parts) {
		case 3:
			auxiliaryAddress = strings.TrimSpace(parts[2])
			if auxiliaryAddress == "" {
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
			auxiliaryAddress = strings.TrimSpace(parts[4])
			if clientID == "" || refreshToken == "" || auxiliaryAddress == "" {
				return nil, importLineError(lineNumber, parts)
			}
		}

		result = append(result, domain.MicrosoftImportLine{
			LineNumber:       lineNumber,
			Email:            email,
			Password:         password,
			ClientID:         clientID,
			RefreshToken:     refreshToken,
			AuxiliaryAddress: auxiliaryAddress,
		})
	}

	if len(result) == 0 {
		return nil, domain.ErrInvalidImportFormat
	}

	return result, nil
}

func importLineError(lineNumber int, parts []string) error {
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
