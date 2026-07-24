package runtimeconfig

import (
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSettingsUpdateImmediately(t *testing.T) {
	Replace([]domain.Setting{{Key: "smtp_outbound_payload_ttl_minutes", Value: "7"}})
	t.Cleanup(func() { Replace(nil) })

	require.Equal(t, 7*time.Minute, Duration("smtp_outbound_payload_ttl_minutes", 5*time.Minute, time.Minute, 1))
	Set("smtp_outbound_payload_ttl_minutes", "9")
	require.Equal(t, 9*time.Minute, Duration("smtp_outbound_payload_ttl_minutes", 5*time.Minute, time.Minute, 1))
	Delete("smtp_outbound_payload_ttl_minutes")
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

func TestRuntimeSettingsRejectUnsafeAndConflictingValues(t *testing.T) {
	require.ErrorIs(t, Validate("alias_generation_window", "2147483647"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("project_name_max", "121"), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("bucket_count", "64"), domain.ErrInvalidKey)
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
