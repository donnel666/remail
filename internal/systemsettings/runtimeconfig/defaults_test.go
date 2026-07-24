package runtimeconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSettingsAreValidAndIndependent(t *testing.T) {
	defaults := DefaultSettings()
	require.Len(t, defaults, DefaultSettingsCount)
	keys := make(map[string]struct{}, len(defaults))
	for _, setting := range defaults {
		if _, exists := keys[setting.Key]; exists {
			t.Fatalf("duplicate default key %q", setting.Key)
		}
		keys[setting.Key] = struct{}{}
		require.NoErrorf(t, Validate(setting.Key, setting.Value), "default %q", setting.Key)
	}
	for _, key := range []string{"bucket_count", "msacl_content_search_window_minutes", "outbound_mail_claim_timeout_minutes"} {
		if _, exists := keys[key]; exists {
			t.Fatalf("removed key %q is still seeded", key)
		}
	}
	whitelistValue := ""
	for _, setting := range defaults {
		if setting.Key == "microsoft_domain_whitelist" {
			whitelistValue = setting.Value
			break
		}
	}
	whitelist := strings.Split(whitelistValue, ",")
	require.Len(t, whitelist, 32)
	require.Equal(t, "outlook.com", whitelist[0])
	require.Equal(t, "outlook.com.vn", whitelist[len(whitelist)-1])
	require.NoError(t, ValidateSnapshot(defaults))

	defaults[0].Value = "changed by caller"
	require.NotEqual(t, "changed by caller", DefaultSettings()[0].Value)
}
