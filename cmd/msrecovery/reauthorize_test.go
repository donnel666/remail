package main

import (
	"context"
	"errors"
	"testing"

	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type reauthorizeProxyProviderStub struct {
	configs   []*proxyapp.ProxyConfig
	requests  []proxyapp.AcquireProxyRequest
	succeeded []uint
	failed    []uint
}

func (s *reauthorizeProxyProviderStub) Acquire(_ context.Context, request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	s.requests = append(s.requests, request)
	index := len(s.requests) - 1
	if index >= len(s.configs) {
		return nil, errors.New("no proxy configured")
	}
	return s.configs[index], nil
}

func (s *reauthorizeProxyProviderStub) ReportSuccess(_ context.Context, proxyID uint) error {
	s.succeeded = append(s.succeeded, proxyID)
	return nil
}

func (s *reauthorizeProxyProviderStub) ReportFailure(_ context.Context, proxyID uint, _ string) error {
	s.failed = append(s.failed, proxyID)
	return nil
}

func TestExecuteReauthorizationWithProxyUsesProductionBindingRoute(t *testing.T) {
	runtimeconfig.Set("max_proxy_attempts", "3")
	t.Cleanup(func() { runtimeconfig.Delete("max_proxy_attempts") })

	proxies := &reauthorizeProxyProviderStub{configs: []*proxyapp.ProxyConfig{{
		ID: 11, ProxyServerID: 101, Pool: proxydomain.ProxyPoolResource,
		URL: "socks5://resource.invalid:1080", IPVersion: proxydomain.ProxyIPv4,
	}}}
	var protocolProxy string
	raw, route, err := executeReauthorizationWithProxy(
		context.Background(),
		proxies,
		commandOptions{RequestID: "request-1"},
		recoverySnapshot{AccountEmail: "OWNER@outlook.com", Password: "password"},
		"proof@recovery.test",
		[]string{"alias@outlook.com"},
		func(_ context.Context, _, _, proxyURL, _ string, _ []string) (msacl.ReauthorizeResult, error) {
			protocolProxy = proxyURL
			return msacl.ReauthorizeResult{Result: msacl.Result{Valid: true}}, nil
		},
	)

	require.NoError(t, err)
	require.True(t, raw.Valid)
	require.Equal(t, "socks5://resource.invalid:1080", protocolProxy)
	require.Equal(t, "resource", route.Label)
	require.Equal(t, 1, route.Attempts)
	require.Len(t, proxies.requests, 1)
	require.Equal(t, "owner@outlook.com", proxies.requests[0].Key)
	require.Equal(t, proxydomain.ProxyIPv4, proxies.requests[0].IPVersion)
	require.Equal(t, proxydomain.ProxyPurposeBinding, proxies.requests[0].Purpose)
	require.True(t, proxies.requests[0].AllowSystemFallback)

	reportReauthorizeProxyOutcome(context.Background(), proxies, route, false, "")
	require.Equal(t, []uint{11}, proxies.succeeded)
}

func TestExecuteReauthorizationWithProxyFailsOverBeforeConsentCleanup(t *testing.T) {
	runtimeconfig.Set("max_proxy_attempts", "3")
	t.Cleanup(func() { runtimeconfig.Delete("max_proxy_attempts") })

	proxies := &reauthorizeProxyProviderStub{configs: []*proxyapp.ProxyConfig{
		{ID: 11, ProxyServerID: 101, Pool: proxydomain.ProxyPoolResource, URL: "socks5://resource.invalid:1080"},
		{ID: 22, ProxyServerID: 202, Pool: proxydomain.ProxyPoolSystem, URL: "socks5://system.invalid:1080"},
	}}
	protocolCalls := 0
	raw, route, err := executeReauthorizationWithProxy(
		context.Background(),
		proxies,
		commandOptions{RequestID: "request-2"},
		recoverySnapshot{AccountEmail: "owner@outlook.com", Password: "password"},
		"proof@recovery.test",
		nil,
		func(_ context.Context, _, _, _ string, _ string, _ []string) (msacl.ReauthorizeResult, error) {
			protocolCalls++
			if protocolCalls == 1 {
				return msacl.ReauthorizeResult{Result: msacl.Result{
					Category: "request", SafeMessage: "temporary", ProxyFailure: true,
				}}, nil
			}
			return msacl.ReauthorizeResult{Result: msacl.Result{Valid: true}}, nil
		},
	)

	require.NoError(t, err)
	require.True(t, raw.Valid)
	require.Equal(t, "system", route.Label)
	require.Equal(t, 2, route.Attempts)
	require.Len(t, proxies.requests, 2)
	require.Equal(t, 1, proxies.requests[1].Attempt)
	require.Equal(t, []uint{101}, proxies.requests[1].AvoidProxyServerIDs)
	require.Equal(t, []uint{11}, proxies.failed)
}

func TestExecuteReauthorizationWithProxyDoesNotReplayAfterCleanupStarts(t *testing.T) {
	proxies := &reauthorizeProxyProviderStub{configs: []*proxyapp.ProxyConfig{{
		ID: 11, ProxyServerID: 101, Pool: proxydomain.ProxyPoolResource, URL: "socks5://resource.invalid:1080",
	}}}
	raw, route, err := executeReauthorizationWithProxy(
		context.Background(),
		proxies,
		commandOptions{RequestID: "request-3"},
		recoverySnapshot{AccountEmail: "owner@outlook.com", Password: "password"},
		"proof@recovery.test",
		nil,
		func(_ context.Context, _, _, _ string, _ string, _ []string) (msacl.ReauthorizeResult, error) {
			return msacl.ReauthorizeResult{
				Result:           msacl.Result{Category: "request", ProxyFailure: true},
				CleanupAttempted: true,
			}, nil
		},
	)

	require.NoError(t, err)
	require.True(t, raw.CleanupAttempted)
	require.Equal(t, 1, route.Attempts)
	require.Len(t, proxies.requests, 1)
	require.Empty(t, proxies.failed)
}

func TestSummarizeReauthorizedAliasesCountsConfirmedAddressesOnce(t *testing.T) {
	results := []msacl.ExplicitAliasResult{
		{Attempted: []string{"first123456@outlook.com"}, Aliases: []string{"first123456@outlook.com"}, Category: "added"},
		{Attempted: []string{"second123456@outlook.com"}, Aliases: []string{"FIRST123456@outlook.com"}, Category: "alias_exists"},
	}

	attempted, confirmed, category := summarizeReauthorizedAliases(results)

	require.Equal(t, 2, attempted)
	require.Equal(t, 1, confirmed)
	require.Equal(t, "alias_exists", category)
	require.Equal(t, []string{"first123456@outlook.com"}, confirmedReauthorizedAliases(results))
}

func TestRejectedOldRefreshTokenCategoryIsNarrow(t *testing.T) {
	require.True(t, isRejectedOldRefreshTokenCategory("oauth_invalid_grant"))
	require.True(t, isRejectedOldRefreshTokenCategory("REFRESH_TOKEN_EXPIRED"))
	require.False(t, isRejectedOldRefreshTokenCategory("request"))
	require.False(t, isRejectedOldRefreshTokenCategory("oauth_permission"))
}

func TestPreferredReauthorizeBindingAcceptsPendingLocalAddressOnly(t *testing.T) {
	snapshot := recoverySnapshot{Binding: &maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingPending,
		BindingAddress: "Pending01@Recovery.Test",
	}}

	require.Equal(t, "pending01@recovery.test", preferredReauthorizeBinding(snapshot, map[string]struct{}{"recovery.test": {}}))
	require.Empty(t, preferredReauthorizeBinding(snapshot, map[string]struct{}{"other.test": {}}))
}

func TestVerifyReauthorizedGraphRetriesIMAPFallbackWithLatestRefreshToken(t *testing.T) {
	previousDelay := graphVerificationRetryDelay
	graphVerificationRetryDelay = 0
	defer func() { graphVerificationRetryDelay = previousDelay }()

	calls := 0
	result, err := verifyReauthorizedGraph(
		context.Background(),
		mailinfra.MicrosoftMailFetchRequest{RefreshToken: "refresh-1", AccessToken: "access-1"},
		func(_ context.Context, request mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error) {
			calls++
			if calls == 1 {
				require.Equal(t, "refresh-1", request.RefreshToken)
				require.Equal(t, "access-1", request.AccessToken)
				return mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "imap", RefreshToken: "refresh-2"}, nil
			}
			require.Equal(t, "refresh-2", request.RefreshToken)
			require.Empty(t, request.AccessToken)
			return mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph", RefreshToken: "refresh-3"}, nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "graph", result.Protocol)
	require.Equal(t, "refresh-3", result.RefreshToken)
}

func TestReauthorizationAuditReportsOnlyCompleteSecurityFlowAsSuccess(t *testing.T) {
	snapshot := recoverySnapshot{ClientID: "old-client", RefreshToken: "old-refresh-token"}

	result, summary := reauthorizationAudit(snapshot, reauthorizeBranchHard, true, true, true, true, true)
	require.Equal(t, "success", result)
	require.Contains(t, summary, "Previous refresh-token rejection was verified.")
	require.Contains(t, summary, "Graph access was verified.")

	result, summary = reauthorizationAudit(snapshot, reauthorizeBranchHard, false, true, false, false, true)
	require.Equal(t, "failure", result)
	require.Contains(t, summary, "Previous refresh-token rejection was not verified.")
	require.Contains(t, summary, "Explicit alias creation was incomplete.")
	require.Contains(t, summary, "Graph access was not verified.")

	result, summary = reauthorizationAudit(snapshot, reauthorizeBranchExternal, true, false, false, false, true)
	require.Equal(t, "success", result)
	require.NotContains(t, summary, "grants were removed")
}
