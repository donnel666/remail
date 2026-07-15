package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

func TestParseCommandOptionsDefaultsToSafeBindingDryRun(t *testing.T) {
	options, err := parseCommandOptions([]string{"-resource-id", "2"}, new(bytes.Buffer))
	require.NoError(t, err)
	require.Equal(t, uint(2), options.ResourceID)
	require.Equal(t, recoveryModeBinding, options.Mode)
	require.False(t, options.Apply)
	require.Zero(t, options.OperatorUserID)
	require.NotEmpty(t, options.RequestID)
	require.Equal(t, 20, generatedPasswordLength)
}

func TestParseCommandOptionsRequiresExactlyOneSelector(t *testing.T) {
	_, err := parseCommandOptions(nil, new(bytes.Buffer))
	require.ErrorContains(t, err, "exactly one")

	_, err = parseCommandOptions([]string{"-resource-id", "2", "-email", "owner@example.test"}, new(bytes.Buffer))
	require.ErrorContains(t, err, "exactly one")
}

func TestParseCommandOptionsKeepsPasswordResetHardDisabled(t *testing.T) {
	t.Setenv("MSRECOVERY_PASSWORD_RESET_ENABLED", "")
	_, err := parseCommandOptions([]string{
		"-resource-id", "2",
		"-mode", recoveryModeReset,
		"-apply",
		"-operator-user-id", "1",
		"-confirm-reset-email", "owner@example.test",
		"-password-artifact", "/tmp/password-artifact",
	}, new(bytes.Buffer))
	require.ErrorContains(t, err, "password reset is disabled")
}

func TestParseCommandOptionsRequiresAllResetConfirmations(t *testing.T) {
	t.Setenv("MSRECOVERY_PASSWORD_RESET_ENABLED", "true")
	_, err := parseCommandOptions([]string{
		"-resource-id", "2",
		"-mode", recoveryModeReset,
		"-apply",
		"-operator-user-id", "1",
	}, new(bytes.Buffer))
	require.ErrorContains(t, err, "confirm-reset-email")

	_, err = parseCommandOptions([]string{
		"-resource-id", "2",
		"-mode", recoveryModeReset,
		"-apply",
		"-operator-user-id", "1",
		"-confirm-reset-email", "owner@example.test",
	}, new(bytes.Buffer))
	require.ErrorContains(t, err, "password-artifact")
}

func TestAllowedBindingAddressRequiresConfiguredLocalDomain(t *testing.T) {
	domains := map[string]struct{}{"recovery.test": {}}
	require.True(t, isAllowedBindingAddress("qalpha01@recovery.test", domains))
	require.False(t, isAllowedBindingAddress("qalpha01@external.test", domains))
	require.False(t, isAllowedBindingAddress("masked-value", domains))
	require.False(t, isAllowedBindingAddress("qa*****@recovery.test", domains))
	require.False(t, isAllowedBindingAddress("Display Name <qalpha01@recovery.test>", domains))
}

func TestPreferredVerifiedBindingRejectsLegacyMaskedValue(t *testing.T) {
	snapshot := recoverySnapshot{Binding: &maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		BindingAddress: "qa*****@recovery.test",
	}}
	require.Empty(t, snapshot.preferredVerifiedBinding())

	snapshot.Binding.BindingAddress = "QAlpha01@Recovery.Test"
	require.Equal(t, "qalpha01@recovery.test", snapshot.preferredVerifiedBinding())
}

func TestConfirmRecoveredBindingRejectsMaskedCandidateBeforeNetwork(t *testing.T) {
	err := confirmRecoveredBinding(context.Background(), recoverySnapshot{
		AccountEmail: "owner@example.test",
		Password:     "private-password",
	}, "qa*****@recovery.test", "")
	require.ErrorContains(t, err, "requires OTP confirmation")
}

func TestCommandResultSerializationHasNoCredentialFields(t *testing.T) {
	result := commandResult{
		Mode:             recoveryModeBinding,
		ResourceID:       2,
		AccountEmail:     "owner@example.test",
		RecoveredBinding: "proof@recovery.test",
	}
	var output bytes.Buffer
	require.NoError(t, writeCommandResult(&output, true, result))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &decoded))
	serialized := strings.ToLower(output.String())
	for _, forbidden := range []string{"password\"", "refresh_token", "client_id", "epid", "recovery_token", "canary", "verification_code"} {
		require.NotContains(t, serialized, forbidden)
	}
}

func TestSameRecoveryAccountRejectsDisabledResources(t *testing.T) {
	resource := &coredomain.MicrosoftResource{
		EmailAddress: "owner@example.test",
		Status:       coredomain.MicrosoftStatusDisabled,
	}
	require.False(t, sameRecoveryAccount(resource, "owner@example.test"))

	resource.Status = coredomain.MicrosoftStatusAbnormal
	require.True(t, sameRecoveryAccount(resource, "OWNER@example.test"))
	require.False(t, sameNormalRecoveryAccount(resource, "OWNER@example.test"))

	resource.Status = coredomain.MicrosoftStatusNormal
	require.True(t, sameNormalRecoveryAccount(resource, "OWNER@example.test"))
}

func TestSameNormalRecoveryAccountRequiresNormalMatchingResource(t *testing.T) {
	tests := []struct {
		name     string
		resource *coredomain.MicrosoftResource
		email    string
		want     bool
	}{
		{name: "nil resource", email: "owner@example.test"},
		{name: "pending", resource: &coredomain.MicrosoftResource{EmailAddress: "owner@example.test", Status: coredomain.MicrosoftStatusPending}, email: "owner@example.test"},
		{name: "abnormal", resource: &coredomain.MicrosoftResource{EmailAddress: "owner@example.test", Status: coredomain.MicrosoftStatusAbnormal}, email: "owner@example.test"},
		{name: "disabled", resource: &coredomain.MicrosoftResource{EmailAddress: "owner@example.test", Status: coredomain.MicrosoftStatusDisabled}, email: "owner@example.test"},
		{name: "deleted", resource: &coredomain.MicrosoftResource{EmailAddress: "owner@example.test", Status: coredomain.MicrosoftStatusDeleted}, email: "owner@example.test"},
		{name: "email mismatch", resource: &coredomain.MicrosoftResource{EmailAddress: "other@example.test", Status: coredomain.MicrosoftStatusNormal}, email: "owner@example.test"},
		{name: "normal case insensitive", resource: &coredomain.MicrosoftResource{EmailAddress: "owner@example.test", Status: coredomain.MicrosoftStatusNormal}, email: "OWNER@example.test", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, sameNormalRecoveryAccount(tt.resource, tt.email))
		})
	}
}

func TestWrapRecoveredBindingConfirmationErrorPreservesCategory(t *testing.T) {
	rateLimited := &msacl.AuthError{
		Message: "Verify recovery code failed (code=1221).",
		Status:  msacl.AuthStatusRateLimited,
	}
	wrapped := wrapRecoveredBindingConfirmationError(rateLimited)
	require.ErrorContains(t, wrapped, "rate_limited")
	require.ErrorIs(t, wrapped, rateLimited)

	temporary := &msacl.AuthError{
		Message: "temporary recovery failure",
		Status:  msacl.AuthStatusRequestError,
	}
	wrapped = wrapRecoveredBindingConfirmationError(temporary)
	require.ErrorContains(t, wrapped, "temporarily unavailable")
	require.ErrorIs(t, wrapped, temporary)
}

func TestFormatRecoveryCommandErrorAddsStableRetryCategory(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		contains string
	}{
		{name: "rate limited", err: &msacl.AuthError{Message: "upstream limited", Status: msacl.AuthStatusRateLimited}, contains: "category=rate_limited"},
		{name: "request", err: &msacl.AuthError{Message: "upstream unavailable", Status: msacl.AuthStatusRequestError}, contains: "category=retryable"},
		{name: "auth timeout", err: &msacl.AuthError{Message: "picker incomplete", Status: msacl.AuthStatusAuthTimeout}, contains: "category=retryable"},
		{name: "code timeout", err: &msacl.AuthError{Message: "mail delayed", Status: msacl.AuthStatusCodeTimeout}, contains: "category=retryable"},
		{name: "deadline", err: context.DeadlineExceeded, contains: "category=retryable"},
		{name: "deterministic", err: &msacl.AuthError{Message: "mailbox unknown", Status: msacl.AuthStatusUnknownMailbox}, contains: "mailbox unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatted := formatRecoveryCommandError(tt.err)
			require.Contains(t, formatted, tt.contains)
			if tt.name == "deterministic" {
				require.NotContains(t, formatted, "category=")
			}
		})
	}
}
