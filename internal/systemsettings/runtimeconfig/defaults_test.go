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
		"bucket_count", "inventory_cache_activity_ttl_minutes", "msacl_content_search_window_minutes", "outbound_mail_claim_timeout_minutes",
		"message_scan_limit", "projection_replay_limit",
		"smsbower_enabled", "smsbower_code_enabled", "smsbower_purchase_enabled", "smsbower_api_key",
		"smsbower_sync_interval_minutes", "smsbower_balance_warning_threshold", "smsbower_points_per_unit", "smsbower_min_margin_rate",
	} {
		if _, exists := keys[key]; exists {
			t.Fatalf("removed key %q is still seeded", key)
		}
	}
	require.Equal(t, "32", keys["background_worker_increase_step"])
	require.Equal(t, "0", keys["registration_reward_amount"])
	require.Equal(t, "86400", keys["session_max_age_seconds"])
	require.Equal(t, "0.8", keys["first_order_rebate_ratio"])
	require.Equal(t, "90", keys["rebate_expiry_days"])
	require.Equal(t, "", keys["domain_custom_tlds"])
	require.Equal(t, "3", keys["domain_max_subdomains_per_registrable_domain"])
	require.Equal(t, "3", keys["proxy_server_health_timeout_seconds"])
	require.Equal(t, "60", keys["proxy_server_health_interval_seconds"])
	require.Equal(t, "120", keys["proxy_server_health_dispatch_lease_seconds"])
	require.Equal(t, "3", keys["proxy_server_failure_threshold"])
	require.Equal(t, "80", keys["proxy_server_inventory_threshold_percent"])
	require.Equal(t, `["(?:^|[^\\d])(\\d{6,8})(?:[^\\d]|$)"]`, keys["verification_code_pattern"])
	require.Equal(t, "8", keys["default_project_gmail_code_price"])
	require.Equal(t, "3", keys["gmail_code_retain_days"])
	require.Equal(t, "300", keys["fetch_dispatcher_timeout_seconds"])
	require.Equal(t, "10000", keys["resource_fetch_dispatch_limit"])
	require.Equal(t, "4", keys["gmail_history_concurrency"])
	require.Equal(t, "4", keys["asynq_queue_mailfetch_weight"])
	require.Equal(t, "1", keys["asynq_queue_background_project_history_weight"])
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
