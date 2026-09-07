package infra

import (
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/proto/domain"
)

const maxEmailLength = 255
const maxPasswordLength = 512

// ParseImport accepts exactly email----password. Delimiters in the password
// are rejected deliberately so malformed rows cannot be interpreted silently.
func ParseImport(content string, strategy string) ([]domain.ImportLine, []domain.ImportLineError, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	if strings.TrimSpace(content) == "" {
		return nil, nil, domain.ErrInvalidImportFormat
	}
	if strategy != domain.ErrorStrategyAbort {
		strategy = domain.ErrorStrategySkip
	}
	var out []domain.ImportLine
	var failures []domain.ImportLineError
	seen := make(map[string]struct{})
	for i, raw := range strings.Split(content, "\n") {
		lineNo := i + 1
		line := strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "----")
		if len(parts) != 2 {
			e := domain.ImportLineError{Line: lineNo, Category: "invalid_format", SafeMessage: "Invalid proto import format."}
			if len(parts) > 0 {
				e.Email = strings.TrimSpace(parts[0])
			}
			if strategy == domain.ErrorStrategyAbort {
				return nil, nil, &e
			}
			failures = append(failures, e)
			continue
		}
		email := strings.ToLower(strings.TrimSpace(parts[0]))
		// Preserve password whitespace exactly. Only the CR line terminator was
		// removed above; trimming a credential changes its meaning.
		password := parts[1]
		if !validEmail(email) || !validPassword(password) {
			e := domain.ImportLineError{Line: lineNo, Email: email, Category: "invalid_format", SafeMessage: "Invalid proto import format."}
			if strategy == domain.ErrorStrategyAbort {
				return nil, nil, &e
			}
			failures = append(failures, e)
			continue
		}
		if _, exists := seen[email]; exists {
			e := domain.ImportLineError{Line: lineNo, Email: email, Category: "duplicate", SafeMessage: "Duplicate Proto email in import file."}
			if strategy == domain.ErrorStrategyAbort {
				return nil, nil, &e
			}
			failures = append(failures, e)
			continue
		}
		seen[email] = struct{}{}
		out = append(out, domain.ImportLine{LineNumber: lineNo, Email: email, Password: password})
	}
	if len(out) == 0 && len(failures) == 0 {
		return nil, nil, domain.ErrInvalidImportFormat
	}
	return out, failures, nil
}

func validPassword(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxPasswordLength && !strings.ContainsAny(value, "\r\n\x00")
}

func validEmail(v string) bool {
	if v == "" || utf8.RuneCountInString(v) > maxEmailLength || strings.Count(v, "@") != 1 || strings.ContainsAny(v, "\r\n\x00") {
		return false
	}
	p, err := mail.ParseAddress(v)
	return err == nil && p.Address == v
}
