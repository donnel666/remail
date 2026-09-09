package infra

import (
	"encoding/base64"
	"io"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra/proton"
)

const maxEmailLength = 255
const maxPasswordLength = 512

// A native PKL is at most 8 MiB; reserve space for UTF-8 credentials and delimiters.
const MaxImportLineBytes = ((proton.MaxPKLBytes + 2) / 3 * 4) + (4 << 10)

// ParseImport accepts email/username----password with an optional Base64 PKL field.
// Password whitespace is significant; supplied PKL is data, never deserialized here.
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
		parts := strings.SplitN(line, "----", 4)
		if (len(parts) != 2 && len(parts) != 3) || len(line) > MaxImportLineBytes {
			e := domain.ImportLineError{Line: lineNo, Category: "invalid_format", SafeMessage: "Invalid proto import format."}
			if email := strings.ToLower(strings.TrimSpace(parts[0])); validEmail(email) {
				e.Email = email
			}
			if strategy == domain.ErrorStrategyAbort {
				return nil, nil, &e
			}
			failures = append(failures, e)
			continue
		}
		rawEmail := strings.ToLower(strings.TrimSpace(parts[0]))
		email := normalizeImportEmail(rawEmail)
		// Preserve password whitespace exactly. Only the CR line terminator was
		// removed above; trimming a credential changes its meaning.
		password := parts[1]
		if !validEmail(email) || !validPassword(password) {
			e := domain.ImportLineError{Line: lineNo, Category: "invalid_format", SafeMessage: "Invalid proto import format."}
			if validEmail(rawEmail) {
				e.Email = rawEmail
			}
			if strategy == domain.ErrorStrategyAbort {
				return nil, nil, &e
			}
			failures = append(failures, e)
			continue
		}
		var pklBase64 string
		if len(parts) == 3 {
			pklBase64 = strings.TrimSpace(parts[2])
			if !validImportPKL(pklBase64) {
				e := domain.ImportLineError{Line: lineNo, Category: "invalid_pkl", SafeMessage: "Proto PKL must be valid standard Base64 and decode to at most 8 MiB."}
				if validEmail(rawEmail) {
					e.Email = rawEmail
				}
				if strategy == domain.ErrorStrategyAbort {
					return nil, nil, &e
				}
				failures = append(failures, e)
				continue
			}
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
		out = append(out, domain.ImportLine{LineNumber: lineNo, Email: email, Password: password, PKLBase64: pklBase64})
	}
	if len(out) == 0 && len(failures) == 0 {
		return nil, nil, domain.ErrInvalidImportFormat
	}
	return out, failures, nil
}

func normalizeImportEmail(value string) string {
	email := strings.ToLower(strings.TrimSpace(value))
	if email != "" && !strings.Contains(email, "@") {
		email += "@proton.me"
	}
	return email
}

func validImportPKL(encoded string) bool {
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(proton.MaxPKLBytes) || strings.ContainsAny(encoded, "\r\n\t ") {
		return false
	}
	// The stream decoder validates each chunk separately; padding is only
	// allowed at the very end of the entire field, never at a chunk boundary.
	if padding := strings.IndexByte(encoded, '='); padding >= 0 && padding < len(encoded)-2 {
		return false
	}
	// Validate the file before abort-mode writes without retaining a second,
	// decoded copy of every PKL. Individual rows are decoded when persisted.
	size, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)))
	return err == nil && size > 0 && size <= proton.MaxPKLBytes
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
