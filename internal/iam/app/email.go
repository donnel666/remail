package app

import (
	"strings"

	"github.com/donnel666/remail/internal/iam/domain"
)

// Domains allowed for self-registration (exact match on the host after @).
var allowedRegistrationDomains = map[string]struct{}{
	"qq.com":         {},
	"foxmail.com":    {},
	"gmail.com":      {},
	"proton.me":      {},
	"protonmail.com": {},
	"pm.me":          {},
	"mail.com":       {},
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validateRegistrationEmail enforces self-registration address rules:
// local part must be ASCII letters/digits only (no punctuation), and the
// domain must be on the supported free-mail list.
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
	if _, allowed := allowedRegistrationDomains[host]; !allowed {
		return domain.ErrRegistrationEmailDomainBlocked
	}
	return nil
}
