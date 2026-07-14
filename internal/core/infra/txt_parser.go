package infra

import (
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/core/domain"
)

const (
	// Keep deterministic row validation aligned with the persistence schema so
	// skip-mode imports reject a bad line before entering a 1000-row transaction.
	microsoftImportEmailMaxLength          = 255
	microsoftImportPasswordMaxLength       = 512
	microsoftImportClientIDMaxLength       = 255
	microsoftImportRefreshTokenMaxLength   = 1024
	microsoftImportBindingAddressMaxLength = 320
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
// Empty lines are skipped. Password bytes between delimiters are preserved.
func (p *TXTParser) ParseMicrosoftImport(content string, strategy domain.ImportErrorStrategy) ([]domain.MicrosoftImportLine, []domain.ImportLineError, error) {
	if strings.TrimSpace(content) == "" {
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
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
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
	password := parts[1]
	if !isValidMicrosoftImportEmail(email, microsoftImportEmailMaxLength) ||
		password == "" ||
		exceedsMicrosoftImportLength(password, microsoftImportPasswordMaxLength) {
		return nil, importLineError(lineNumber, parts)
	}

	clientID := ""
	refreshToken := ""
	bindingAddress := ""
	switch len(parts) {
	case 3:
		bindingAddress = strings.TrimSpace(parts[2])
		if !isValidMicrosoftImportEmail(bindingAddress, microsoftImportBindingAddressMaxLength) {
			return nil, importLineError(lineNumber, parts)
		}
	case 4:
		clientID = strings.TrimSpace(parts[2])
		refreshToken = strings.TrimSpace(parts[3])
		if clientID == "" ||
			refreshToken == "" ||
			exceedsMicrosoftImportLength(clientID, microsoftImportClientIDMaxLength) ||
			exceedsMicrosoftImportLength(refreshToken, microsoftImportRefreshTokenMaxLength) {
			return nil, importLineError(lineNumber, parts)
		}
	case 5:
		clientID = strings.TrimSpace(parts[2])
		refreshToken = strings.TrimSpace(parts[3])
		bindingAddress = strings.TrimSpace(parts[4])
		if clientID == "" ||
			refreshToken == "" ||
			exceedsMicrosoftImportLength(clientID, microsoftImportClientIDMaxLength) ||
			exceedsMicrosoftImportLength(refreshToken, microsoftImportRefreshTokenMaxLength) ||
			!isValidMicrosoftImportEmail(bindingAddress, microsoftImportBindingAddressMaxLength) {
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

func isValidMicrosoftImportEmail(value string, maxLength int) bool {
	if value == "" || exceedsMicrosoftImportLength(value, maxLength) || strings.Count(value, "@") != 1 {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

func exceedsMicrosoftImportLength(value string, maxLength int) bool {
	return maxLength <= 0 || utf8.RuneCountInString(value) > maxLength
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
