package runtimeconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSettingsAreValidAndIndependent(t *testing.T) {
	defaults := DefaultSettings()
	require.Len(t, defaults, DefaultSettingsCount)
	keys := make(map[string]string, len(defaults))
	for _, setting := range defaults {
		if _, exists := keys[setting.Key]; exists {
			t.Fatalf("duplicate default key %q", setting.Key)
		}
		keys[setting.Key] = setting.Value
		require.NoErrorf(t, Validate(setting.Key, setting.Value), "default %q", setting.Key)
	}
	for _, key := range []string{
		"admin_resource_list_max_limit", "admin_log_max_limit", "admin_task_max_limit", "admin_message_max_limit",
		"api_key_meta_ttl_seconds", "api_key_cache_flush_interval_seconds",
		"bucket_count", "msacl_content_search_window_minutes", "outbound_mail_claim_timeout_minutes",
	} {
		if _, exists := keys[key]; exists {
			t.Fatalf("removed key %q is still seeded", key)
		}
	}
	require.Equal(t, "32", keys["background_worker_increase_step"])
	require.Equal(t, "0", keys["registration_reward_amount"])
	require.Equal(t, "86400", keys["session_max_age_seconds"])
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
