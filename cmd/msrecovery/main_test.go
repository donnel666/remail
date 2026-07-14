package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
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
	require.ErrorContains(t, err, "requires password confirmation")
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
}
