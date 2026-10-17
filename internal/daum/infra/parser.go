package infra

import (
	"net/mail"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/daum/domain"
)

const maxEmailLength = 255
const maxPasswordLength = 512

// CanonicalEmail identifies one account while leaving delivered addresses intact.
func CanonicalEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	local, suffix, ok := strings.Cut(value, "@")
	if !ok || !validEmail(value) || (suffix != "daum.net" && suffix != "hanmail.net" && suffix != "hanmail2.net") {
		return "", false
	}
	// Every account creates both aliases; validate their stored lengths before
	// accepting a shorter input suffix such as daum.net.
	for _, fixedSuffix := range []string{"daum.net", "hanmail.net", "hanmail2.net"} {
		if utf8.RuneCountInString(local+"@"+fixedSuffix) > maxEmailLength {
			return "", false
		}
	}
	return local + "@daum.net", true
}

func ParseImport(content, strategy string) ([]domain.ImportLine, []domain.ImportLineError, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	if strings.TrimSpace(content) == "" {
		return nil, nil, domain.ErrInvalidImportFormat
	}
	var rows []domain.ImportLine
	var failures []domain.ImportLineError
	seen := make(map[string]bool)
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "----")
		row := domain.ImportLine{LineNumber: i + 1, Protocol: "imap"}
		failure := domain.ImportLineError{Line: i + 1, Category: "invalid_format", SafeMessage: "Expected email----APP password, email----password----APP password, or five fields including remail URL and Kakao email."}
		valid := len(parts) == 2 || len(parts) == 3 || len(parts) == 5
		if valid {
			row.Email, valid = CanonicalEmail(parts[0])
			if len(parts) == 2 {
				row.AppPassword = parts[1]
			} else {
				row.Password, row.AppPassword = parts[1], parts[2]
			}
			valid = valid && validPassword(row.AppPassword) && (len(parts) == 2 || validPassword(row.Password))
			if len(parts) == 5 {
				row.PickupURL, row.KakaoEmail = strings.TrimSpace(parts[3]), strings.ToLower(strings.TrimSpace(parts[4]))
				var err error
				row.BindingAddress, _, err = ParsePickupURL(row.PickupURL)
				valid = valid && err == nil && validEmail(row.KakaoEmail)
			}
		}
		if valid && seen[row.Email] {
			valid = false
			failure.Category, failure.SafeMessage = "duplicate", "Duplicate Daum account in import file."
		}
		if !valid {
			if email := strings.ToLower(strings.TrimSpace(parts[0])); validEmail(email) {
				failure.Email = email
			}
			if strategy == domain.ErrorStrategyAbort {
				return nil, nil, &failure
			}
			failures = append(failures, failure)
			continue
		}
		seen[row.Email] = true
		rows = append(rows, row)
	}
	return rows, failures, nil
}

// ParsePickupURL only parses a remail capability; callers never fetch its host.
func ParsePickupURL(value string) (email, token string, err error) {
	u, parseErr := url.Parse(value)
	if parseErr != nil || len(value) > 4096 || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Path != "/pickup" && u.Path != "/v1/pickup") {
		return "", "", domain.ErrInvalidImportFormat
	}
	query, queryErr := url.ParseQuery(u.RawQuery)
	if queryErr != nil || len(query["email"]) != 1 || len(query["token"]) != 1 {
		return "", "", domain.ErrInvalidImportFormat
	}
	email, token = strings.ToLower(strings.TrimSpace(query.Get("email"))), query.Get("token")
	if !validEmail(email) || token == "" || len(token) > 2048 || strings.ContainsAny(token, "\r\n\x00") {
		return "", "", domain.ErrInvalidImportFormat
	}
	return email, token, nil
}

func validPassword(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxPasswordLength && !strings.ContainsAny(value, "\r\n\x00")
}
func validEmail(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > maxEmailLength || strings.Count(value, "@") != 1 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}
func validProtocol(value string) bool { return value == "imap" || value == "pop3" }
