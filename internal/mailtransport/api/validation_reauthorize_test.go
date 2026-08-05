package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftValidationAliasesUseProvisionableOutlookDomain(t *testing.T) {
	candidates, err := microsoftValidationAliasCandidates()

	require.NoError(t, err)
	require.Len(t, candidates, microsoftValidationAliasCount)
	for _, candidate := range candidates {
		require.True(t, strings.HasSuffix(candidate, "@outlook.com"))
	}
}

func completeHardReauthorizeResult(aliases ...string) msacl.ReauthorizeResult {
	return msacl.ReauthorizeResult{
		Result: msacl.Result{
			Valid: true, ClientID: "new-client", AccessToken: "new-access", RefreshToken: "new-refresh",
		},
		CleanupAttempted: true,
		ConsentCleanup:   msacl.ConsentCleanupResult{Remaining: 0},
		AliasResults:     []msacl.ExplicitAliasResult{{Aliases: aliases}},
	}
}

func TestHardReauthorizeSecurityGates(t *testing.T) {
	candidates := []string{"first@outlook.com", "second@outlook.com"}
	req := coreapp.MicrosoftValidationRequest{
		EmailAddress: "owner@outlook.com", ClientID: "old-client", RefreshToken: "old-refresh",
	}

	t.Run("success requires aliases and rejected old RT", func(t *testing.T) {
		oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{Category: "oauth_invalid_grant"}}
		fetcher := &microsoftMailFetcherStub{result: mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph"}}
		adapter := &ResourceValidationAdapter{microsoft: oauth, fetcher: fetcher}

		result, _, err := adapter.finishMicrosoftHardReauthorize(context.Background(), req, "", candidates, completeHardReauthorizeResult(candidates...))

		require.NoError(t, err)
		require.True(t, result.Valid)
		require.True(t, result.CredentialsAuthoritative)
		require.ElementsMatch(t, candidates, result.ConfirmedAliases)
		require.Equal(t, 1, oauth.calls)
		require.Equal(t, 1, fetcher.calls)
	})

	t.Run("old RT still valid is terminal", func(t *testing.T) {
		oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{Valid: true, ClientID: "old-client", RefreshToken: "rotated-old-refresh"}}
		adapter := &ResourceValidationAdapter{microsoft: oauth}

		result, _, err := adapter.finishMicrosoftHardReauthorize(context.Background(), req, "", candidates, completeHardReauthorizeResult(candidates...))

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Equal(t, "old_rt_still_valid", result.Category)
	})

	t.Run("missing alias is terminal", func(t *testing.T) {
		result, _, err := (&ResourceValidationAdapter{}).finishMicrosoftHardReauthorize(
			context.Background(), req, "", candidates, completeHardReauthorizeResult(candidates[0]),
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Equal(t, "alias_incomplete", result.Category)
		require.Equal(t, []string{candidates[0]}, result.ConfirmedAliases)
	})

	t.Run("alias rate limit remains identifiable", func(t *testing.T) {
		raw := completeHardReauthorizeResult()
		raw.AliasResults = []msacl.ExplicitAliasResult{{
			Category: "rate_limited", SafeMessage: "Microsoft alias creation is rate limited.", ProxyFailure: true,
		}}

		result, proxyFailure, err := (&ResourceValidationAdapter{}).finishMicrosoftHardReauthorize(
			context.Background(), req, "", candidates, raw,
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Equal(t, "rate_limited", result.Category)
		require.Equal(t, "Microsoft alias creation is rate limited.", result.SafeMessage)
		require.True(t, proxyFailure)
	})

	t.Run("external binding retries supplied RT and retains mask", func(t *testing.T) {
		oauth := &microsoftOAuthProtocolStub{refreshFn: func(request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
			switch request.RefreshToken {
			case "old-refresh":
				return mailinfra.MicrosoftOAuthResult{Category: "oauth_invalid_grant", SafeMessage: "Microsoft refresh token is invalid or expired."}, nil
			case "file-refresh":
				return mailinfra.MicrosoftOAuthResult{
					Valid: true, ClientID: "file-client", AccessToken: "fallback-access", RefreshToken: "rotated-file-refresh",
				}, nil
			default:
				t.Fatalf("unexpected refresh token")
				return mailinfra.MicrosoftOAuthResult{}, nil
			}
		}}
		fetcher := &microsoftMailFetcherStub{result: mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph"}}
		adapter := &ResourceValidationAdapter{microsoft: oauth, fetcher: fetcher}
		ctx := WithMicrosoftValidationFallbackOAuthCredentials(context.Background(), req.EmailAddress, "file-client", "file-refresh")

		result, _, err := adapter.finishMicrosoftHardReauthorize(ctx, req, "", candidates, msacl.ReauthorizeResult{
			ExternalBinding: true,
			Result: msacl.Result{
				BindingAddress: "m*****d@external.test",
				BindingStatus:  "failed",
				SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
			},
		})

		require.NoError(t, err)
		require.True(t, result.Valid)
		require.True(t, result.CredentialsAuthoritative)
		require.Equal(t, "file-client", result.ClientID)
		require.Equal(t, "rotated-file-refresh", result.RefreshToken)
		require.Empty(t, result.ConfirmedAliases)
		require.Equal(t, &coreapp.MicrosoftBindingObservation{
			Address:     "m*****d@external.test",
			Status:      "failed",
			SafeMessage: "Microsoft account is already bound to another recovery mailbox.",
		}, result.BindingObservation)
		require.Equal(t, 2, oauth.calls)
		require.Equal(t, "old-refresh", oauth.requests[0].RefreshToken)
		require.Equal(t, "file-refresh", oauth.requests[1].RefreshToken)
		require.Equal(t, 1, fetcher.calls)
	})

	t.Run("external binding skips supplied RT when token is unchanged", func(t *testing.T) {
		oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
			Category: "oauth_invalid_grant", SafeMessage: "Microsoft refresh token is invalid or expired.",
		}}
		adapter := &ResourceValidationAdapter{microsoft: oauth}
		ctx := WithMicrosoftValidationFallbackOAuthCredentials(context.Background(), req.EmailAddress, "file-client", req.RefreshToken)

		result, _, err := adapter.finishMicrosoftHardReauthorize(ctx, req, "", candidates, msacl.ReauthorizeResult{
			ExternalBinding: true,
			Result: msacl.Result{
				BindingAddress: "m*****d@external.test",
				BindingStatus:  "failed",
				SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
			},
		})

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Equal(t, "oauth_invalid_grant", result.Category)
		require.NotNil(t, result.BindingObservation)
		require.Equal(t, 1, oauth.calls)
	})
}

func TestHardReauthorizeRateLimitBeforeRemovalRotatesProxy(t *testing.T) {
	runtimeconfig.Set("max_proxy_attempts", "1")
	t.Cleanup(func() { runtimeconfig.Delete("max_proxy_attempts") })
	proxies := &microsoftProxyProviderStub{acquireFn: func(request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
		serverID := uint(request.Attempt + 1)
		return &proxyapp.ProxyConfig{ID: serverID * 10, ProxyServerID: serverID, URL: fmt.Sprintf("socks5://server-%d.invalid:1080", serverID)}, nil
	}}
	calls := 0
	adapter := &ResourceValidationAdapter{
		proxies: proxies,
		fetcher: &microsoftMailFetcherStub{result: mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph"}},
		hardReauthorize: func(_ context.Context, _, _, _, _ string, aliases []string) (msacl.ReauthorizeResult, error) {
			calls++
			if calls == 1 {
				return msacl.ReauthorizeResult{
					Result:           msacl.Result{Category: "request", SafeMessage: "Microsoft authorization is temporarily rate limited.", ProxyFailure: true},
					CleanupAttempted: true,
				}, nil
			}
			return completeHardReauthorizeResult(aliases...), nil
		},
	}

	result, err := adapter.validateMicrosoftHardReauthorize(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID: 1, EmailAddress: "owner@outlook.com", Password: "password", RequestID: "validation-1",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Len(t, proxies.requests, 2)
	require.Empty(t, proxies.requests[0].AvoidProxyServerIDs)
	require.Equal(t, []uint{1}, proxies.requests[1].AvoidProxyServerIDs)
	require.Equal(t, []uint{10}, proxies.failures)
}
