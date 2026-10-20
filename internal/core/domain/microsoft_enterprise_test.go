package domain

import "testing"

func TestMicrosoftEnterpriseEmail(t *testing.T) {
	for _, email := range []string{"user@tenant.onmicrosoft.com", "USER@TENANT-123.ONMICROSOFT.COM"} {
		if !IsMicrosoftEnterpriseEmail(email) {
			t.Errorf("rejected tenant email %q", email)
		}
	}
	for _, email := range []string{"user@outlook.com", "user@onmicrosoft.com", "user@.onmicrosoft.com", "user@a.b.onmicrosoft.com", "user@-a.onmicrosoft.com", "user@a-.onmicrosoft.com", "user@tenant.onmicrosoft.com.evil.test", "user@tenant_onmicrosoft.com", "user@@tenant.onmicrosoft.com"} {
		if IsMicrosoftEnterpriseEmail(email) {
			t.Errorf("accepted invalid tenant email %q", email)
		}
	}
}
