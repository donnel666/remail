package domain

import "strings"

// Enterprise inventory only accepts a single tenant label under onmicrosoft.com.
func IsMicrosoftEnterpriseEmail(email string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@")
	if len(parts) != 2 || parts[0] == "" || !strings.HasSuffix(parts[1], ".onmicrosoft.com") {
		return false
	}
	tenant := strings.TrimSuffix(parts[1], ".onmicrosoft.com")
	if len(tenant) == 0 || len(tenant) > 63 || tenant[0] == '-' || tenant[len(tenant)-1] == '-' {
		return false
	}
	for _, ch := range tenant {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
			return false
		}
	}
	return true
}
