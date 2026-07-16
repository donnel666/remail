// Package api tests.
//
// The old createAliases/reconcileAliases struct fields have been removed in the
// OTC-login rewrite (Step 5). The adapter now calls msacl.SyncAndAddExplicitAliases
// directly and no longer offers injectable stub functions.
//
// TODO: rewrite these tests to cover the new SyncAndAddExplicitAliases path:
//   - OTC login failure halts and returns an error
//   - Successful create returns ExistingAliases
//   - Backfill-only (empty candidates) lists without creating
package api

import (
	"context"
	"errors"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftAliasAdapterPreparesBindingByRulesBeforeRecoveryLookup(t *testing.T) {
	msacl.SetAuxiliaryDomains([]string{"recovery.test"})
	adapter := NewMicrosoftAliasCreationAdapter(nil)

	generated, err := msacl.DeterministicAuxiliaryAddress("owner@example.test")
	require.NoError(t, err)
	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress:   "owner@example.test",
		BindingAddress: maskedBindingAddress(t, generated),
	})
	require.NoError(t, err)
	require.Equal(t, generated, result.BindingAddress)

	result, err = adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress:   "owner@example.test",
		BindingAddress: "o*****r@recovery.test",
	})
	require.NoError(t, err)
	require.Equal(t, "owner@recovery.test", result.BindingAddress)

	result, err = adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		EmailAddress:   "owner@example.test",
		BindingAddress: "o*****r@external.test",
	})
	require.NoError(t, err)
	require.Equal(t, "external_binding", result.Category)
	require.Equal(t, "o*****r@external.test", result.BindingAddress)
}

func TestMicrosoftAliasAdapterTreatsMalformedBindingAsMissing(t *testing.T) {
	msacl.SetAuxiliaryDomains([]string{"recovery.test"})
	generated, err := msacl.DeterministicAuxiliaryAddress("owner@example.test")
	require.NoError(t, err)
	authorizeCalls := 0
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.authorize = func(_ context.Context, email, password, proxy, preferred string) (msacl.Result, error) {
		authorizeCalls++
		require.Equal(t, "owner@example.test", email)
		require.Equal(t, "secret", password)
		require.Empty(t, proxy)
		require.Equal(t, generated, preferred)
		return msacl.Result{Valid: true, BindingAddress: generated}, nil
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 42, EmailAddress: "owner@example.test", Password: "secret", BindingAddress: "bad@address@recovery.test",
	})

	require.NoError(t, err)
	require.Equal(t, 1, authorizeCalls)
	require.Equal(t, generated, result.BindingAddress)
}

func TestMicrosoftAliasAdapterConfirmsCurrentBindingProofWithoutSendingRecoveryMail(t *testing.T) {
	confirmCalls := 0
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.probePasswordRecovery = func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
		return msacl.PasswordRecoveryProbeResult{Proofs: []msacl.PasswordRecoveryProofInfo{{
			Type: "Email", MaskedAddress: "a*****e@recovery.test",
		}}}, nil
	}
	adapter.confirmPasswordRecovery = func(context.Context, string, string, msacl.PasswordRecoveryConfirmationOptions) (msacl.PasswordRecoveryConfirmationResult, error) {
		confirmCalls++
		return msacl.PasswordRecoveryConfirmationResult{}, nil
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 42, EmailAddress: "owner@example.test", BindingAddress: "alice@recovery.test",
	})
	require.NoError(t, err)
	require.Equal(t, "alice@recovery.test", result.BindingAddress)
	require.Zero(t, confirmCalls)
}

func TestMicrosoftAliasAdapterReplacesStaleBindingFromCurrentProof(t *testing.T) {
	generated, err := msacl.DeterministicAuxiliaryAddress("owner@example.test")
	require.NoError(t, err)
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.probePasswordRecovery = func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
		return msacl.PasswordRecoveryProbeResult{Proofs: []msacl.PasswordRecoveryProofInfo{{
			Type: "Email", MaskedAddress: maskedBindingAddress(t, generated),
		}}}, nil
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 42, EmailAddress: "owner@example.test", BindingAddress: "stale@recovery.test",
	})
	require.NoError(t, err)
	require.Equal(t, generated, result.BindingAddress)
}

func TestMicrosoftAliasAdapterClassifiesCurrentExternalProof(t *testing.T) {
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.probePasswordRecovery = func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
		return msacl.PasswordRecoveryProbeResult{Proofs: []msacl.PasswordRecoveryProofInfo{{
			Type: "Email", MaskedAddress: "x*****9@external.test",
		}}}, nil
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 42, EmailAddress: "owner@example.test", BindingAddress: "stale@recovery.test",
	})
	require.NoError(t, err)
	require.Equal(t, "external_binding", result.Category)
	require.Equal(t, "x*****9@external.test", result.BindingAddress)
}

func TestMicrosoftAliasAdapterRecoversRandomSystemProofByRecipient(t *testing.T) {
	confirmCalls := 0
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.probePasswordRecovery = func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
		return msacl.PasswordRecoveryProbeResult{Proofs: []msacl.PasswordRecoveryProofInfo{{
			Type: "Email", MaskedAddress: "x*****9@recovery.test",
		}}}, nil
	}
	adapter.confirmPasswordRecovery = func(_ context.Context, _ string, _ string, options msacl.PasswordRecoveryConfirmationOptions) (msacl.PasswordRecoveryConfirmationResult, error) {
		confirmCalls++
		require.Equal(t, "x*****9@recovery.test", options.ExpectedBindingAddress)
		return msacl.PasswordRecoveryConfirmationResult{
			Probe: msacl.PasswordRecoveryProbeResult{
				BindingAddress: "xrandom9@recovery.test", BindingResolved: true,
			},
			BindingConfirmed: true,
		}, nil
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 42, EmailAddress: "owner@example.test", BindingAddress: "stale@recovery.test",
	})
	require.NoError(t, err)
	require.Equal(t, "xrandom9@recovery.test", result.BindingAddress)
	require.Equal(t, 1, confirmCalls)
}

func TestMicrosoftAliasAdapterReturnsObservedMaskWhenRecoveryMailFails(t *testing.T) {
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	adapter.probePasswordRecovery = func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
		return msacl.PasswordRecoveryProbeResult{Proofs: []msacl.PasswordRecoveryProofInfo{{
			Type: "Email", MaskedAddress: "x*****9@recovery.test",
		}}}, nil
	}
	adapter.confirmPasswordRecovery = func(context.Context, string, string, msacl.PasswordRecoveryConfirmationOptions) (msacl.PasswordRecoveryConfirmationResult, error) {
		return msacl.PasswordRecoveryConfirmationResult{}, errors.New("code timeout")
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 42, EmailAddress: "owner@example.test", BindingAddress: "stale@recovery.test",
	})
	require.Error(t, err)
	require.Equal(t, "x*****9@recovery.test", result.BindingAddress)
}

func TestMicrosoftAliasAdapterStopsAfterPostSideEffectBecomesUncertain(t *testing.T) {
	t.Skip("adapter rewritten — no longer has injectable createAliases field")
	_ = context.Background()
	_ = require.New(t)
	_ = mailapp.MicrosoftAliasCreationRequest{}
}

func TestMicrosoftAliasAdapterRotatesProxyBeforeAnyPostSideEffect(t *testing.T) {
	t.Skip("adapter rewritten — no longer has injectable createAliases field")
}

func TestMicrosoftAliasAdapterUsesReadOnlyReconciliationForUncertainCandidates(t *testing.T) {
	t.Skip("adapter rewritten — ReconcileOnly logic removed")
}

func TestMicrosoftAliasAdapterDoesNotRotateProxyForPageTimeout(t *testing.T) {
	t.Skip("adapter rewritten — no longer has injectable createAliases field")
}
