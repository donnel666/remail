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
	require.ErrorIs(t, Validate("verification_code_pattern", "("), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("verification_code_pattern", `\ `), domain.ErrInvalidValue)
	require.NoError(t, Validate("microsoft_domain_whitelist", "outlook.com,hotmail.com"))
	require.ErrorIs(t, Validate("microsoft_domain_whitelist", "https://outlook.com"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("microsoft_domain_whitelist", "a.-invalid.com"), domain.ErrInvalidValue)
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
	require.ErrorIs(t, Validate("bcrypt_cost", "3"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("bcrypt_cost", "17"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("email_code_digit_len", "11"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("session_max_age_seconds", "299"), domain.ErrInvalidValue)
}

func TestValidateGlobalNoticeSize(t *testing.T) {
	require.NoError(t, Validate("global_notice", strings.Repeat("界", maxSystemNoticeBytes/3)+"x"))
	require.ErrorIs(t, Validate("global_notice", strings.Repeat("界", maxSystemNoticeBytes/3)+"xx"), domain.ErrInvalidValue)
}

func TestValidateSystemOperationsSettings(t *testing.T) {
	require.NoError(t, Validate("background_load_overload_percent", "11"))
	require.ErrorIs(t, Validate("background_load_overload_percent", "10"), domain.ErrInvalidValue)
	require.NoError(t, Validate("retention_daily_run_hour", "23"))
	require.ErrorIs(t, Validate("retention_daily_run_hour", "24"), domain.ErrInvalidValue)
	require.NoError(t, Validate("slow_request_threshold_ms", "0"))
	for _, key := range []string{
		"admin_resource_list_max_limit", "admin_log_max_limit", "admin_task_max_limit", "admin_message_max_limit",
		"api_key_meta_ttl_seconds", "api_key_cache_flush_interval_seconds",
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
