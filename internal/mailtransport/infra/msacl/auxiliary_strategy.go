package msacl

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

var auxiliaryTokenRE = regexp.MustCompile(`[^a-z0-9]`)

func isProjectMailboxAddress(mailbox string) bool {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if mailbox == "" {
		return false
	}
	if strings.Contains(mailbox, "@") {
		_, domain, ok := strings.Cut(mailbox, "@")
		return ok && domainInProject(domain)
	}
	return domainInProject(mailbox)
}

func deterministicAuxiliaryAddress(accountEmail string) (string, error) {
	domain, err := mailDomain()
	if err != nil {
		return "", err
	}
	return deterministicAuxiliaryAddressForDomain(accountEmail, domain)
}

func deterministicAuxiliaryAddressForDomain(accountEmail, domainName string) (string, error) {
	normalizedEmail := strings.ToLower(strings.TrimSpace(accountEmail))
	local, sourceDomain, ok := strings.Cut(normalizedEmail, "@")
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	if !ok || local == "" || sourceDomain == "" || domainName == "" {
		return "", newAuthError("无法从账号邮箱生成辅助邮箱", AuthStatusRequestError)
	}
	return buildAuxiliaryLocalPart(normalizedEmail, sourceDomain) + "@" + domainName, nil
}

func buildAuxiliaryLocalPart(normalizedEmail, sourceDomain string) string {
	sum := sha256.Sum256([]byte(normalizedEmail))
	digest := fmt.Sprintf("%x", sum)
	return buildAuxiliaryPrefix(sourceDomain) + digest[:12]
}

func buildAuxiliaryPrefix(sourceDomain string) string {
	parts := strings.Split(strings.ToLower(sourceDomain), ".")
	lastPart := lastNonBlankPart(parts)
	lastToken := sanitizeAuxiliaryToken(lastPart)
	var prefix strings.Builder
	for i := 0; i < len(parts)-1; i++ {
		token := sanitizeAuxiliaryToken(parts[i])
		if token != "" {
			prefix.WriteByte(token[0])
		}
	}
	if prefix.Len() == 0 {
		prefix.WriteByte('m')
	}
	tail := lastToken
	if tail == "" {
		tail = "mail"
	}
	return prefix.String() + tail + "_"
}

func lastNonBlankPart(parts []string) string {
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.TrimSpace(parts[i]) != "" {
			return parts[i]
		}
	}
	return ""
}

func sanitizeAuxiliaryToken(value string) string {
	return auxiliaryTokenRE.ReplaceAllString(strings.ToLower(value), "")
}

func mailboxMatchesMasked(maskedEmail, auxiliary string) bool {
	maskedEmail = strings.ToLower(strings.TrimSpace(maskedEmail))
	auxiliary = strings.ToLower(strings.TrimSpace(auxiliary))
	if maskedEmail == "" || auxiliary == "" {
		return true
	}
	maskLocal, maskDomain, maskOK := strings.Cut(maskedEmail, "@")
	auxLocal, auxDomain, auxOK := strings.Cut(auxiliary, "@")
	if !maskOK || !auxOK {
		return auxiliary == maskedEmail
	}
	if maskDomain != auxDomain || auxLocal == "" {
		return false
	}
	var pattern strings.Builder
	pattern.WriteString("^")
	for _, r := range maskLocal {
		if r == '*' {
			pattern.WriteString(".*")
			continue
		}
		pattern.WriteString(regexp.QuoteMeta(string(r)))
	}
	pattern.WriteString("@")
	pattern.WriteString(regexp.QuoteMeta(maskDomain))
	pattern.WriteString("$")
	ok, err := regexp.MatchString(pattern.String(), auxiliary)
	return err == nil && ok
}
