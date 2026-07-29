package app

import (
	"net/mail"
	"strings"
	"unicode"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

const defaultRegistrationEmailWhitelist = "qq.com,foxmail.com,gmail.com,proton.me,protonmail.com,pm.me,mail.com"

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateEmailAddress(email string) error {
	normalized := normalizeEmail(email)
	address, err := mail.ParseAddress(normalized)
	if err != nil || address.Address == "" || !strings.EqualFold(address.Address, normalized) {
		return domain.ErrInvalidEmailAddress
	}
	at := strings.LastIndex(normalized, "@")
	if at <= 0 || at == len(normalized)-1 {
		return domain.ErrInvalidEmailAddress
	}
	host := normalized[at+1:]
	if !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return domain.ErrInvalidEmailAddress
	}
	if host == "invalid" || strings.HasSuffix(host, ".invalid") {
		return domain.ErrInvalidEmailAddress
	}
	return nil
}

func trustedLinuxDOEmail(email string) string {
	normalized := normalizeEmail(email)
	if validateEmailAddress(normalized) != nil {
		return ""
	}
	return normalized
}

func validateLinuxDOEmail(email, providerEmail string, mode LinuxDOAccountMode) error {
	normalized := normalizeEmail(email)
	switch mode {
	case LinuxDOAccountExisting:
		return validateEmailAddress(normalized)
	case LinuxDOAccountNew:
		if trusted := trustedLinuxDOEmail(providerEmail); trusted != "" && normalized == trusted {
			return nil
		}
		return validateRegistrationEmail(normalized)
	default:
		return domain.ErrLinuxDOAccountModeInvalid
	}
}

// validateRegistrationEmail enforces self-registration address rules:
// local part must be ASCII letters/digits only (no punctuation), and the
// domain must be on the runtime registration whitelist.
func validateRegistrationEmail(email string) error {
	normalized := normalizeEmail(email)
	at := strings.LastIndex(normalized, "@")
	if at <= 0 || at == len(normalized)-1 {
		return domain.ErrRegistrationEmailLocalInvalid
	}
	local, host := normalized[:at], normalized[at+1:]
	if local == "" || host == "" || strings.Contains(host, " ") {
		return domain.ErrRegistrationEmailLocalInvalid
	}
	// ASCII alnum only — matches frontend and rejects every symbol.
	for i := 0; i < len(local); i++ {
		c := local[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return domain.ErrRegistrationEmailLocalInvalid
		}
	}
	allowed := false
	for _, candidate := range strings.FieldsFunc(runtimeconfig.String("registration_email_whitelist", defaultRegistrationEmailWhitelist), func(r rune) bool {
		return r == ',' || r == '，' || unicode.IsSpace(r)
	}) {
		candidate = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".")
		if candidate == host {
			allowed = true
			break
		}
	}
	if !allowed {
		return domain.ErrRegistrationEmailDomainBlocked
	}
	return nil
}
