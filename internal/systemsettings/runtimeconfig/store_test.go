package runtimeconfig

import (
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSettingsUpdateImmediately(t *testing.T) {
	Replace([]domain.Setting{{Key: "SMTP_OUTBOUND_PAYLOAD_TTL_MINUTES", Value: "7"}, {Key: "CAPTCHA_ENABLED", Value: "false"}})
	t.Cleanup(func() { Replace(nil) })

	require.Equal(t, 7*time.Minute, Duration("smtp_outbound_payload_ttl_minutes", 5*time.Minute, time.Minute, 1))
	require.False(t, Bool("captcha_enabled", true))
	Set("SMTP_OUTBOUND_PAYLOAD_TTL_MINUTES", "9")
	require.Equal(t, 9*time.Minute, Duration("smtp_outbound_payload_ttl_minutes", 5*time.Minute, time.Minute, 1))
	Delete("SMTP_OUTBOUND_PAYLOAD_TTL_MINUTES")
	require.Equal(t, 5*time.Minute, Duration("smtp_outbound_payload_ttl_minutes", 5*time.Minute, time.Minute, 1))
}

func TestSnapshotKeepsRelatedValuesFromOneVersion(t *testing.T) {
	Replace([]domain.Setting{{Key: "smtp_task_retry_count", Value: "1"}})
	t.Cleanup(func() { Replace(nil) })
	values := Snapshot()
	Set("smtp_task_retry_count", "2")

	require.Equal(t, 1, values.Int("smtp_task_retry_count", 3, 0))
	require.Equal(t, 2, Int("smtp_task_retry_count", 3, 0))
}

func TestDurationPreservesNonIntegralFallback(t *testing.T) {
	require.Equal(t, 1500*time.Millisecond, Duration("missing_duration", 1500*time.Millisecond, time.Second, 1))
	require.Equal(t, 1500*time.Millisecond, Duration("missing_duration", 1500*time.Millisecond, 0, 1))
}

func TestValidateEmailServiceSettings(t *testing.T) {
	require.NoError(t, Validate("token_refresh_hour", "23"))
	require.ErrorIs(t, Validate("token_refresh_hour", "24"), domain.ErrInvalidValue)
	require.NoError(t, Validate("verification_code_pattern", `(^|[^\d])(\d{6})([^\d]|$)`))
	require.NoError(t, Validate("verification_code_pattern", `["href=[\"'](https?://[^\"']+)[\"']","(\\d{6,8})"]`))
	require.ErrorIs(t, Validate("verification_code_pattern", `[]`), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("verification_code_pattern", `["(\\d{6})",""]`), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("verification_code_pattern", `["("]`), domain.ErrInvalidValue)
	require.NoError(t, Validate("verification_code_pattern", `[123]`))
	require.ErrorIs(t, Validate("verification_code_pattern", "("), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("verification_code_pattern", `\ `), domain.ErrInvalidValue)
	require.NoError(t, Validate("microsoft_domain_whitelist", "outlook.com,hotmail.com"))
	require.ErrorIs(t, Validate("microsoft_domain_whitelist", "https://outlook.com"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("microsoft_domain_whitelist", "a.-invalid.com"), domain.ErrInvalidValue)
	require.NoError(t, Validate("domain_custom_tlds", "edu.kg,edu.invalid"))
	require.ErrorIs(t, Validate("domain_custom_tlds", "kg"), domain.ErrInvalidValue)
	require.NoError(t, Validate("domain_max_subdomains_per_registrable_domain", "3"))
	require.ErrorIs(t, Validate("domain_max_subdomains_per_registrable_domain", "0"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("smtp_outbound_payload_ttl_minutes", "0"), domain.ErrInvalidValue)
	require.NoError(t, Validate("smtp_task_retry_count", "0"))
}

func TestValidateAuthSecuritySettings(t *testing.T) {
	require.NoError(t, Validate("register_enabled", "false"))
	require.ErrorIs(t, Validate("captcha_enabled", "1"), domain.ErrInvalidValue)
	require.NoError(t, Validate("registration_email_whitelist", "qq.com, gmail.com"))
	require.ErrorIs(t, Validate("registration_email_whitelist", "https://qq.com"), domain.ErrInvalidValue)
	require.NoError(t, Validate("registration_reward_amount", "12.345678"))
	require.ErrorIs(t, Validate("registration_reward_amount", "-0.01"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("registration_reward_amount", "0.0000001"), domain.ErrInvalidValue)
	require.NoError(t, Validate("default_project_microsoft_code_price", "0.000001"))
	require.ErrorIs(t, Validate("default_project_microsoft_code_price", "-0.01"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("default_project_microsoft_code_price", "0.0000001"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("bcrypt_cost", "3"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("bcrypt_cost", "17"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("email_code_digit_len", "11"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("session_max_age_seconds", "299"), domain.ErrInvalidValue)
	require.NoError(t, Validate("linuxdo_minimum_trust_level", "4"))
	require.ErrorIs(t, Validate("linuxdo_minimum_trust_level", "5"), domain.ErrInvalidValue)
	require.NoError(t, Validate("linuxdo_callback_url", "https://mail.example.com/v1/oauth/linuxdo/callback"))
	require.NoError(t, Validate("linuxdo_callback_url", "http://localhost:8080/v1/oauth/linuxdo/callback"))
	require.ErrorIs(t, Validate("linuxdo_callback_url", "http://mail.example.com/v1/oauth/linuxdo/callback"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("linuxdo_callback_url", "https://mail.example.com/v1/oauth/linuxdo/callback?"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("linuxdo_callback_url", "https://mail.example.com/v1/oauth/linuxdo/callback?next=/"), domain.ErrInvalidValue)
	require.NoError(t, Validate("github_callback_url", "https://mail.example.com/v1/oauth/github/callback"))
	require.NoError(t, Validate("github_callback_url", "http://localhost:8080/v1/oauth/github/callback"))
	require.ErrorIs(t, Validate("github_callback_url", "https://mail.example.com/v1/oauth/linuxdo/callback"), domain.ErrInvalidValue)
	require.NoError(t, Validate("github_minimum_account_age_days", "365"))
	require.ErrorIs(t, Validate("github_minimum_account_age_days", "-1"), domain.ErrInvalidValue)

	missingSecret := ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "linuxdo_oauth_enabled", Value: "true"},
		{Key: "linuxdo_client_id", Value: "client-id"},
		{Key: "linuxdo_callback_url", Value: "https://mail.example.com/v1/oauth/linuxdo/callback"},
	})
	var fields *domain.InvalidValueFieldsError
	require.ErrorAs(t, missingSecret, &fields)
	require.Contains(t, fields.Fields, "linuxdo_client_secret")
	require.NoError(t, ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "linuxdo_oauth_enabled", Value: "true"},
		{Key: "linuxdo_client_id", Value: "client-id"},
		{Key: "linuxdo_client_secret", Value: "client-secret"},
		{Key: "linuxdo_callback_url", Value: "https://mail.example.com/v1/oauth/linuxdo/callback"},
	}))

	missingGitHubSecret := ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "github_oauth_enabled", Value: "true"},
		{Key: "github_client_id", Value: "client-id"},
		{Key: "github_callback_url", Value: "https://mail.example.com/v1/oauth/github/callback"},
	})
	require.ErrorAs(t, missingGitHubSecret, &fields)
	require.Contains(t, fields.Fields, "github_client_secret")
	require.NoError(t, ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "github_oauth_enabled", Value: "true"},
		{Key: "github_client_id", Value: "client-id"},
		{Key: "github_client_secret", Value: "client-secret"},
		{Key: "github_callback_url", Value: "https://mail.example.com/v1/oauth/github/callback"},
	}))
}

func TestValidateRechargeRebateSettings(t *testing.T) {
	require.NoError(t, Validate("first_order_rebate_ratio", "0.8"))
	require.NoError(t, Validate("single_rebate_cap", "100.50"))
	require.NoError(t, Validate("cumulative_rebate_cap", "0"))
	require.NoError(t, Validate("rebate_expiry_days", "0"))
	require.ErrorIs(t, Validate("first_order_rebate_ratio", "1.000001"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("single_rebate_cap", "-0.01"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("rebate_expiry_days", "36501"), domain.ErrInvalidValue)
}

func TestValidateDailyRewardSettings(t *testing.T) {
	require.NoError(t, Validate("daily_checkin_reward_rules", `[{"amount":100,"probability":0.005},{"amount":50,"probability":0.1}]`))
	require.NoError(t, Validate("daily_checkin_reward_rules", `[{"amount":100,"probability":0.8},{"amount":50,"probability":0.3}]`))
	require.NoError(t, Validate("daily_checkin_reward_rules", `[{"amount":100,"probability":10},{"amount":50,"probability":20}]`))
	require.ErrorIs(t, Validate("daily_checkin_reward_rules", `[{"amount":100,"probability":9223372036854.775807},{"amount":50,"probability":0.000001}]`), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("daily_checkin_reward_rules", `[{"amount":100.5,"probability":1}]`), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("daily_checkin_reward_rules", `[{"amount":100,"probability":0.0000001}]`), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("daily_checkin_reward_rules", `[{"amount":100,"probability":0.8},{"amount":100.0,"probability":0.2}]`), domain.ErrInvalidValue)
	rules, err := ParseCheckinRewardRules(`[{"amount":10,"probability":0.7},{"amount":1000,"probability":0.1},{"amount":500,"probability":0.2}]`)
	require.NoError(t, err)
	require.Equal(t, []string{"1000.00", "500.00", "10.00"}, []string{rules[0].Amount, rules[1].Amount, rules[2].Amount})
	require.Equal(t, []int64{100_000, 200_000, 700_000}, []int64{rules[0].ProbabilityUnits, rules[1].ProbabilityUnits, rules[2].ProbabilityUnits})
	require.NoError(t, Validate("leaderboard_reward_rules", `[{"rankFrom":1,"rankTo":1,"amount":100},{"rankFrom":2,"rankTo":6,"amount":20}]`))
	require.ErrorIs(t, Validate("leaderboard_reward_rules", `[{"rankFrom":1,"rankTo":3,"amount":100},{"rankFrom":3,"rankTo":6,"amount":20}]`), domain.ErrInvalidValue)
	require.NoError(t, Validate("leaderboard_settlement_time", "00:00"))
	require.ErrorIs(t, Validate("leaderboard_settlement_time", "24:00"), domain.ErrInvalidValue)
	require.ErrorIs(t, ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{{Key: "daily_checkin_enabled", Value: "true"}}), domain.ErrInvalidValue)
}

func TestValidateRechargePaymentSettings(t *testing.T) {
	require.NoError(t, Validate("epay_version", "v1"))
	require.NoError(t, Validate("epay_version", "v2"))
	require.ErrorIs(t, Validate("epay_version", "v3"), domain.ErrInvalidValue)
	require.NoError(t, Validate("epay_gateway_url", "https://pay.example.com/"))
	require.ErrorIs(t, Validate("epay_gateway_url", "http://pay.example.com/"), domain.ErrInvalidValue)
	require.NoError(t, Validate("redemption_code_purchase_url", "https://shop.example.com/cards"))
	require.ErrorIs(t, Validate("redemption_code_purchase_url", "javascript:alert(1)"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("topup_amount_presets", "[10,20.5]"), domain.ErrInvalidValue)
	require.NoError(t, Validate("topup_amount_presets", "[10,20.00]"))
	require.ErrorIs(t, Validate("topup_amount_presets", "[10,10.00]"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("topup_amount_bonus", `{"20.5":2}`), domain.ErrInvalidValue)
	require.NoError(t, Validate("topup_amount_bonus", `{"20":0.5}`))
	require.ErrorIs(t, Validate("topup_amount_bonus", `{"20.5":-1}`), domain.ErrInvalidValue)
	require.NoError(t, Validate("topup_fee_cap", "0.009"))
	require.NoError(t, Validate("points_per_yuan", "1000"))
	require.ErrorIs(t, Validate("points_per_yuan", "0"), domain.ErrInvalidValue)
	require.NoError(t, Validate("points_unit_migration_v1", "completed"))
	require.ErrorIs(t, Validate("points_unit_migration_v1", "pending"), domain.ErrInvalidValue)
	require.NoError(t, Validate("max_pending_recharge_orders", "10"))
	require.ErrorIs(t, Validate("max_pending_recharge_orders", "0"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("max_pending_recharge_orders", "101"), domain.ErrInvalidValue)
	err := ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{{Key: "epay_enabled", Value: "true"}})
	require.ErrorIs(t, err, domain.ErrInvalidValue)
	var fieldError *domain.InvalidValueFieldsError
	require.ErrorAs(t, err, &fieldError)
	require.Contains(t, fieldError.Fields, "epay_gateway_url")
	require.Contains(t, fieldError.Fields, "epay_merchant_key")
	err = ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "epay_enabled", Value: "true"},
		{Key: "epay_version", Value: "v2"},
		{Key: "epay_gateway_url", Value: "https://pay.example.com/"},
		{Key: "epay_merchant_id", Value: "1000"},
		{Key: "epay_notify_url", Value: "https://app.example.com/v1/payments/webhooks/epay/v2"},
		{Key: "epay_return_url", Value: "https://app.example.com/wallet"},
	})
	require.ErrorAs(t, err, &fieldError)
	require.Equal(t, map[string]string{
		"epay_private_key":         "Required when EPay is enabled.",
		"epay_platform_public_key": "Required when EPay is enabled.",
	}, fieldError.Fields)
	require.NoError(t, ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "epay_enabled", Value: "true"},
		{Key: "epay_gateway_url", Value: "https://pay.example.com/"},
		{Key: "epay_merchant_id", Value: "1000"},
		{Key: "epay_merchant_key", Value: "secret"},
		{Key: "epay_notify_url", Value: "https://app.example.com/v1/payments/webhooks/epay/v1"},
		{Key: "epay_return_url", Value: "https://app.example.com/wallet"},
	}))
	require.NoError(t, ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "epay_enabled", Value: "true"},
		{Key: "epay_version", Value: "v2"},
		{Key: "epay_gateway_url", Value: "https://pay.example.com/"},
		{Key: "epay_merchant_id", Value: "1000"},
		{Key: "epay_private_key", Value: "merchant-private-key"},
		{Key: "epay_platform_public_key", Value: "platform-public-key"},
		{Key: "epay_notify_url", Value: "https://app.example.com/v1/payments/webhooks/epay/v2"},
		{Key: "epay_return_url", Value: "https://app.example.com/wallet"},
	}))
}

func TestValidateGlobalNoticeSize(t *testing.T) {
	require.NoError(t, Validate("global_notice", strings.Repeat("界", maxSystemNoticeBytes/3)+"x"))
	require.ErrorIs(t, Validate("global_notice", strings.Repeat("界", maxSystemNoticeBytes/3)+"xx"), domain.ErrInvalidValue)
}

func TestValidateSystemOperationsSettings(t *testing.T) {
	require.NoError(t, Validate("background_load_overload_percent", "11"))
	require.ErrorIs(t, Validate("background_load_overload_percent", "10"), domain.ErrInvalidValue)
	require.NoError(t, Validate("project_history_concurrency", "8096"))
	require.ErrorIs(t, Validate("project_history_concurrency", "8097"), domain.ErrInvalidValue)
	require.NoError(t, Validate("resource_fetch_dispatch_limit", "10000"))
	require.ErrorIs(t, Validate("resource_fetch_dispatch_limit", "10001"), domain.ErrInvalidValue)
	require.NoError(t, Validate("fetch_dispatcher_timeout_seconds", "3600"))
	require.ErrorIs(t, Validate("fetch_dispatcher_timeout_seconds", "3601"), domain.ErrInvalidValue)
	for _, key := range []string{
		"background_worker_minimum", "background_worker_initial", "background_worker_increase_step",
		"asynq_worker_concurrency", "asynq_realtime_worker_concurrency", "asynq_background_worker_concurrency",
	} {
		require.NoError(t, Validate(key, "8096"))
		require.ErrorIs(t, Validate(key, "8097"), domain.ErrInvalidValue)
	}
	for _, key := range []string{
		"asynq_queue_mailfetch_weight", "asynq_queue_payment_reconcile_weight", "asynq_queue_mailtransport_weight", "asynq_queue_default_weight",
		"asynq_queue_background_validation_weight", "asynq_queue_background_domain_validation_weight", "asynq_queue_background_alias_weight",
		"asynq_queue_background_token_refresh_weight", "asynq_queue_resource_weight", "asynq_queue_background_project_history_weight", "asynq_queue_background_inventory_weight",
	} {
		require.NoError(t, Validate(key, "10000"))
		require.ErrorIs(t, Validate(key, "10001"), domain.ErrInvalidValue)
	}
	require.NoError(t, Validate("retention_daily_run_hour", "23"))
	require.ErrorIs(t, Validate("retention_daily_run_hour", "24"), domain.ErrInvalidValue)
	require.NoError(t, Validate("slow_request_threshold_ms", "0"))
	require.NoError(t, Validate("proxy_server_health_timeout_seconds", "10"))
	require.ErrorIs(t, Validate("proxy_server_health_timeout_seconds", "11"), domain.ErrInvalidValue)
	require.NoError(t, Validate("proxy_server_inventory_threshold_percent", "100"))
	require.ErrorIs(t, Validate("proxy_server_inventory_threshold_percent", "101"), domain.ErrInvalidValue)
	for _, key := range []string{
		"admin_resource_list_max_limit", "admin_log_max_limit", "admin_task_max_limit", "admin_message_max_limit",
		"api_key_meta_ttl_seconds", "api_key_cache_flush_interval_seconds",
		"inventory_cache_activity_ttl_minutes",
	} {
		require.ErrorIs(t, Validate(key, "100"), domain.ErrInvalidKey)
	}
	require.ErrorIs(t, ValidatePersistedUpdates(DefaultSettings(), []domain.Setting{
		{Key: "background_worker_minimum", Value: "32"},
		{Key: "background_worker_initial", Value: "16"},
	}), domain.ErrInvalidValue)
}

func TestRuntimeSettingsRejectUnsafeAndConflictingValues(t *testing.T) {
	require.ErrorIs(t, Validate("alias_generation_window", "2147483647"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("project_name_max", "121"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("bucket_count", "64"), domain.ErrInvalidKey)
	require.ErrorIs(t, Validate("BUCKET_COUNT", "64"), domain.ErrInvalidKey)
	require.ErrorIs(t, ValidateUpdates([]domain.Setting{
		{Key: "pickup_fetch_reserve_ttl_minutes", Value: "1"},
		{Key: "pickup_fetch_lease_ttl_minutes", Value: "1"},
		{Key: "pickup_fetch_heartbeat_seconds", Value: "120"},
	}), domain.ErrInvalidValue)
	require.ErrorIs(t, ValidateUpdates([]domain.Setting{
		{Key: "recovery_code_lease_minutes", Value: "1"},
		{Key: "password_recovery_code_wait_seconds", Value: "90"},
	}), domain.ErrInvalidValue)
	require.ErrorIs(t, ValidateUpdates([]domain.Setting{
		{Key: "smtp_task_retry_count", Value: "20"},
		{Key: "outbound_mail_timeout_minutes", Value: "13"},
	}), domain.ErrInvalidValue)
	require.NoError(t, ValidateUpdates([]domain.Setting{
		{Key: "smtp_task_retry_count", Value: "20"},
		{Key: "outbound_mail_timeout_minutes", Value: "14"},
		{Key: "smtp_outbound_payload_ttl_minutes", Value: "14"},
	}))
}

func TestRuntimeDefaultSettingsCannotBeDeleted(t *testing.T) {
	require.ErrorIs(t, ValidateDelete("SMTP_TASK_RETRY_COUNT"), domain.ErrInvalidValue)
	require.ErrorIs(t, ValidateDelete("points_unit_migration_v1"), domain.ErrInvalidValue)
	require.NoError(t, ValidateDelete("custom.setting"))
}

func TestReplaceFallsBackFromConflictingPersistedValues(t *testing.T) {
	Replace([]domain.Setting{
		{Key: "pickup_fetch_reserve_ttl_minutes", Value: "1"},
		{Key: "pickup_fetch_lease_ttl_minutes", Value: "1"},
		{Key: "pickup_fetch_heartbeat_seconds", Value: "120"},
	})
	t.Cleanup(func() { Replace(nil) })

	require.Equal(t, 2*time.Minute, Duration("pickup_fetch_lease_ttl_minutes", 2*time.Minute, time.Minute, 1))
	require.Equal(t, 30*time.Second, Duration("pickup_fetch_heartbeat_seconds", 30*time.Second, time.Second, 1))
}
