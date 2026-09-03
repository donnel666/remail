// Package api tests.
//
// The old createAliases/reconcileAliases struct fields have been removed in the
// OTC-login rewrite (Step 5). The adapter now calls msacl.SyncAndAddExplicitAliases
// directly and no longer offers injectable stub functions.
//
// TODO: add injectable browser functions if adapter-level network sequencing
// needs coverage beyond the msacl sequence tests.
package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftAliasAdapterUsesActiveOutlookCandidateDomain(t *testing.T) {
	adapter := NewMicrosoftAliasCreationAdapter(nil)
	for _, test := range []struct {
		account string
		suffix  string
	}{
		{account: "owner@hotmail.com", suffix: "@outlook.com"},
		{account: "owner@outlook.com", suffix: "@outlook.com"},
		{account: "owner@outlook.fr", suffix: "@outlook.com"},
		{account: "owner@outlook.com.au", suffix: "@outlook.com.au"},
		{account: "owner@outlook.cl", suffix: "@outlook.cl"},
		{account: "owner@outlook.de", suffix: "@outlook.de"},
		{account: "owner@outlook.es", suffix: "@outlook.es"},
		{account: "owner@outlook.jp", suffix: "@outlook.jp"},
		{account: "owner@outlook.my", suffix: "@outlook.my"},
		{account: "owner@outlook.pt", suffix: "@outlook.pt"},
		{account: "owner@outlook.sa", suffix: "@outlook.com"},
		{account: "owner@outlook.com.br", suffix: "@outlook.com"},
		{account: "owner@outlook.be", suffix: "@outlook.com"},
	} {
		candidates, err := adapter.GenerateMicrosoftAliasCandidates(2, test.account)
		require.NoError(t, err)
		require.Len(t, candidates, 2)
		for _, candidate := range candidates {
			require.Truef(t, strings.HasSuffix(candidate, test.suffix), "candidate=%q suffix=%q", candidate, test.suffix)
		}
	}
	_, err := adapter.GenerateMicrosoftAliasCandidates(2, "bad@address@outlook.fr")
	require.Error(t, err)
}

func TestAuthorizeAliasBindingAvoidsFailedProxyServerOnRetry(t *testing.T) {
	proxies := &microsoftProxyProviderStub{acquireFn: func(request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
		serverID := uint(len(request.AvoidProxyServerIDs) + 1)
		return &proxyapp.ProxyConfig{
			ID: serverID * 10, ProxyServerID: serverID, URL: fmt.Sprintf("socks5://server-%d.invalid:1080", serverID),
		}, nil
	}}
	calls := 0
	adapter := &MicrosoftAliasCreationAdapter{
		proxies: proxies,
		authorize: func(context.Context, string, string, string, string) (msacl.Result, error) {
			calls++
			if calls == 1 {
				return msacl.Result{ProxyFailure: true, SafeMessage: "Proxy failed."}, nil
			}
			return msacl.Result{Valid: true, BindingAddress: "binding@recovery.test"}, nil
		},
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 7, RequestID: "alias-request", EmailAddress: "owner@example.test", Password: "secret",
	})

	require.NoError(t, err)
	require.Equal(t, "binding@recovery.test", result.BindingAddress)
	require.Len(t, proxies.requests, 2)
	require.Empty(t, proxies.requests[0].AvoidProxyServerIDs)
	require.Equal(t, []uint{1}, proxies.requests[1].AvoidProxyServerIDs)
}

func TestAuthorizeAliasBindingDoesNotFailProxyForTaskError(t *testing.T) {
	proxies := &microsoftProxyProviderStub{acquireFn: func(proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
		return &proxyapp.ProxyConfig{ID: 10, ProxyServerID: 1, URL: "socks5://server.invalid:1080"}, nil
	}}
	adapter := &MicrosoftAliasCreationAdapter{
		proxies: proxies,
		authorize: func(context.Context, string, string, string, string) (msacl.Result, error) {
			return msacl.Result{
				Category:    "request",
				SafeMessage: "Microsoft alias service is temporarily unavailable.",
			}, errors.New("response parsing failed")
		},
	}

	result, err := adapter.PrepareMicrosoftAliasBinding(context.Background(), mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 8, EmailAddress: "owner@example.test", Password: "secret",
	})

	require.Error(t, err)
	require.Equal(t, "request", result.Category)
	require.Len(t, proxies.requests, 1)
	require.Empty(t, proxies.failures)
}

func TestMicrosoftAliasBindingLeavesProxyNeutralWhenAcquireFails(t *testing.T) {
	acquireErr := errors.New("proxy repository unavailable")
	proxies := &microsoftProxyProviderStub{acquireFn: func(proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
		return nil, acquireErr
	}}
	adapter := &MicrosoftAliasCreationAdapter{proxies: proxies}
	req := mailapp.MicrosoftAliasCreationRequest{
		ResourceID: 9, EmailAddress: "owner@example.test", Password: "secret",
	}

	checks := []func() (mailapp.MicrosoftAliasBindingPreparationResult, error){
		func() (mailapp.MicrosoftAliasBindingPreparationResult, error) {
			return adapter.authorizeAliasBinding(context.Background(), req, "binding@recovery.test", "")
		},
		func() (mailapp.MicrosoftAliasBindingPreparationResult, error) {
			return adapter.recoverAliasBindingViaPasswordRecovery(context.Background(), req, "b*****g@recovery.test")
		},
		func() (mailapp.MicrosoftAliasBindingPreparationResult, error) {
			return adapter.confirmCurrentAliasBinding(context.Background(), req, "binding@recovery.test")
		},
	}
	for _, check := range checks {
		result, err := check()
		require.ErrorIs(t, err, acquireErr)
		require.False(t, result.ProxyFailure)
		require.Equal(t, "request", result.Category)
	}

	require.Len(t, proxies.requests, len(checks))
	require.Empty(t, proxies.failures)
	require.Empty(t, proxies.successes)
}

func TestConfirmedAddedAliasesRejectsFailedAndRateLimitedResults(t *testing.T) {
	confirmed := confirmedAddedAliases([]msacl.ExplicitAliasResult{
		{Aliases: []string{"ADDED@OUTLOOK.COM"}, Category: "added"},
		{Aliases: []string{"failed@outlook.com"}, Category: "failed"},
		{Aliases: []string{"limited@outlook.com"}, Category: "rate_limited"},
		{Aliases: []string{"exists@outlook.com"}, Category: "exists"},
	})

	require.Equal(t, []string{"added@outlook.com"}, confirmed)
}

func TestNormalizeMicrosoftAliasesDeduplicatesReconciliationResult(t *testing.T) {
	aliases := normalizeMicrosoftAliases([]string{
		"FIRST@OUTLOOK.COM", " first@outlook.com ", "second@outlook.com",
	})

	require.Equal(t, []string{"first@outlook.com", "second@outlook.com"}, aliases)
}

func TestSummarizeMicrosoftAliasAddResultsPreservesAttemptedProxyFailure(t *testing.T) {
	result, proxyFailure := summarizeMicrosoftAliasAddResults([]msacl.ExplicitAliasResult{
		{Aliases: []string{"FIRST@OUTLOOK.COM"}, Attempted: []string{"FIRST@OUTLOOK.COM"}, Category: "added"},
		{
			Attempted: []string{"second@outlook.com"}, Category: "request",
			SafeMessage: "Microsoft alias service is temporarily unavailable.", ProxyFailure: true,
		},
	})

	require.True(t, proxyFailure)
	require.Equal(t, []string{"first@outlook.com"}, result.Aliases)
	require.Equal(t, []string{"first@outlook.com", "second@outlook.com"}, result.Attempted)
	require.Equal(t, []string{"second@outlook.com"}, result.Uncertain)
	require.Equal(t, "request", result.Category)
}

func TestSummarizeMicrosoftAliasAddResultsPreservesEarlierUncertainWhenLaterCandidateSucceeds(t *testing.T) {
	result, proxyFailure := summarizeMicrosoftAliasAddResults([]msacl.ExplicitAliasResult{
		{Attempted: []string{"uncertain@outlook.com"}, Category: "request"},
		{Aliases: []string{"added@outlook.com"}, Attempted: []string{"added@outlook.com"}, Category: "added"},
	})

	require.False(t, proxyFailure)
	require.Equal(t, []string{"added@outlook.com"}, result.Aliases)
	require.Equal(t, []string{"uncertain@outlook.com", "added@outlook.com"}, result.Attempted)
	require.Equal(t, []string{"uncertain@outlook.com"}, result.Uncertain)
	require.Equal(t, "added", result.Category)
}

func TestAliasCreationSummaryPreservesListedAliasesForBackfill(t *testing.T) {
	result := mailapp.MicrosoftAliasCreationResult{
		ExistingAliases: normalizeMicrosoftAliases([]string{"Existing@Outlook.com"}),
	}

	require.Equal(t, []string{"existing@outlook.com"}, result.ExistingAliases)
}

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
