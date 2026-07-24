package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func init() {
	msacl.SetAuxiliaryDomains([]string{"recovery.test"})
}

type microsoftOAuthProtocolStub struct {
	result          mailinfra.MicrosoftOAuthResult
	err             error
	refreshFn       func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error)
	request         mailinfra.MicrosoftOAuthRequest
	requests        []mailinfra.MicrosoftOAuthRequest
	calls           int
	acquireResult   mailinfra.MicrosoftOAuthResult
	acquireErr      error
	acquireFn       func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error)
	acquireRequest  mailinfra.MicrosoftOAuthRequest
	acquireRequests []mailinfra.MicrosoftOAuthRequest
	acquireCalls    int
}

func (s *microsoftOAuthProtocolStub) RefreshToken(_ context.Context, request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
	s.calls++
	s.request = request
	s.requests = append(s.requests, request)
	if s.refreshFn != nil {
		return s.refreshFn(request)
	}
	return s.result, s.err
}

func (s *microsoftOAuthProtocolStub) AcquireToken(_ context.Context, request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
	s.acquireCalls++
	s.acquireRequest = request
	s.acquireRequests = append(s.acquireRequests, request)
	if s.acquireFn != nil {
		return s.acquireFn(request)
	}
	return s.acquireResult, s.acquireErr
}

type microsoftMailFetcherStub struct {
	result   mailinfra.MicrosoftMailFetchResult
	err      error
	fetchFn  func(mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error)
	request  mailinfra.MicrosoftMailFetchRequest
	requests []mailinfra.MicrosoftMailFetchRequest
	calls    int
}

func (s *microsoftMailFetcherStub) FetchAll(_ context.Context, request mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error) {
	s.calls++
	s.request = request
	s.requests = append(s.requests, request)
	if s.fetchFn != nil {
		return s.fetchFn(request)
	}
	return s.result, s.err
}

type microsoftProxyProviderStub struct {
	acquireFn      func(proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	requests       []proxyapp.AcquireProxyRequest
	successes      []uint
	failures       []uint
	failureMessage []string
}

func (s *microsoftProxyProviderStub) Acquire(_ context.Context, request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	s.requests = append(s.requests, request)
	if s.acquireFn != nil {
		return s.acquireFn(request)
	}
	return &proxyapp.ProxyConfig{Direct: true}, nil
}

func (s *microsoftProxyProviderStub) ReportSuccess(_ context.Context, proxyID uint) error {
	s.successes = append(s.successes, proxyID)
	return nil
}

func (s *microsoftProxyProviderStub) ReportFailure(_ context.Context, proxyID uint, safeError string) error {
	s.failures = append(s.failures, proxyID)
	s.failureMessage = append(s.failureMessage, safeError)
	return nil
}

type microsoftValidationBindingStoreStub struct {
	binding *maildomain.MicrosoftBindingMailbox
	findErr error
}

func (s *microsoftValidationBindingStoreStub) FindByResourceIDs(_ context.Context, resourceIDs []uint) (map[uint]maildomain.MicrosoftBindingMailbox, error) {
	result := make(map[uint]maildomain.MicrosoftBindingMailbox)
	if s.findErr != nil {
		return nil, s.findErr
	}
	if s.binding != nil && len(resourceIDs) > 0 {
		result[resourceIDs[0]] = *s.binding
	}
	return result, nil
}

func TestResourceValidationFetchUsesLightweightMailboxProbe(t *testing.T) {
	fetcher := &microsoftMailFetcherStub{result: mailinfra.MicrosoftMailFetchResult{Valid: true}}
	adapter := &ResourceValidationAdapter{fetcher: fetcher}

	result, err := adapter.fetchMicrosoftValidation(context.Background(), "main@example.com", "", mailinfra.MicrosoftOAuthResult{
		Valid: true, ClientID: "client", RefreshToken: "refresh",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, 1, fetcher.request.MaxMessages)
}

func TestMicrosoftValidationBindingErrorClassification(t *testing.T) {
	for _, err := range []error{
		mailinfra.ErrMicrosoftBindingRecoveryConflict,
		mailinfra.ErrMicrosoftBindingRecoveryResourceNotFound,
		mailinfra.ErrMicrosoftBindingRecoveryResourceDeleted,
	} {
		require.ErrorIs(t, mapMicrosoftValidationBindingError(err), coreapp.ErrValidationResultStale)
	}
	for _, err := range []error{
		mailinfra.ErrMicrosoftBindingRecoveryIneligible,
		mailinfra.ErrMicrosoftBindingAddressOccupied,
	} {
		require.ErrorIs(t, mapMicrosoftValidationBindingError(err), coreapp.ErrValidationBindingRejected)
	}
	passthrough := errors.New("database unavailable")
	require.ErrorIs(t, mapMicrosoftValidationBindingError(passthrough), passthrough)
}

func TestMicrosoftTokenRefreshACLReturnsRotatedCredentialsButNeverAccessToken(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
		Valid:        true,
		ClientID:     "rotated-client-id",
		RefreshToken: "rotated-refresh-token",
		AccessToken:  "access-token-canary",
	}}
	adapter := &ResourceValidationAdapter{microsoft: oauth}
	result, err := adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{
		ResourceID:   42,
		EmailAddress: "main@example.com",
		ClientID:     "original-client-id",
		RefreshToken: "original-refresh-token",
		RequestID:    "request-42",
	})
	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, "rotated-client-id", result.ClientID)
	require.Equal(t, "rotated-refresh-token", result.RefreshToken)
	require.Equal(t, "Microsoft refresh-token diagnostic succeeded.", result.SafeMessage)
	require.Equal(t, "original-client-id", oauth.request.ClientID)
	require.Equal(t, "original-refresh-token", oauth.request.RefreshToken)
}

func TestMicrosoftTokenRefreshACLUsesFixedSafeFailureMessages(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
		Category:    "oauth_invalid_grant",
		SafeMessage: "raw upstream body refresh-token-canary access-token-canary",
	}}
	adapter := &ResourceValidationAdapter{microsoft: oauth}
	result, err := adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{
		EmailAddress: "main@example.com",
		ClientID:     "client-canary",
		RefreshToken: "refresh-token-canary",
	})
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "oauth_invalid_grant", result.Category)
	require.Equal(t, "Microsoft refresh token is invalid or expired.", result.SafeMessage)
	require.NotContains(t, result.SafeMessage, "canary")

	oauth.result = mailinfra.MicrosoftOAuthResult{
		Category:     "unrecognized-upstream-category",
		SafeMessage:  "database and token internals",
		ProxyFailure: true,
	}
	result, err = adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{})
	require.NoError(t, err)
	require.Equal(t, maxMicrosoftProxyAttempts+2, oauth.calls)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "Microsoft mail service is temporarily unavailable.", result.SafeMessage)
}

func TestMicrosoftTokenRefreshACLConvertsProtocolErrorsToSafeRetryableResult(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{err: errors.New("upstream response contained refresh-token-canary")}
	adapter := &ResourceValidationAdapter{microsoft: oauth}
	result, err := adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{})
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "Microsoft mail service is temporarily unavailable.", result.SafeMessage)
	require.NotContains(t, result.SafeMessage, "canary")
}

func TestMicrosoftTokenRefreshACLUsesRuntimeProxyAttemptLimit(t *testing.T) {
	runtimeconfig.Set("max_proxy_attempts", "1")
	t.Cleanup(func() { runtimeconfig.Delete("max_proxy_attempts") })
	oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
		Category:     "request",
		ProxyFailure: true,
	}}
	adapter := &ResourceValidationAdapter{microsoft: oauth}

	_, err := adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{})

	require.NoError(t, err)
	require.Equal(t, 2, oauth.calls)
}

func TestRecoverBindingForValidationReturnsCandidateWithFenceOnlyWhenEligible(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	var preferred string
	var proxy string
	adapter := &ResourceValidationAdapter{
		bindings: &microsoftValidationBindingStoreStub{},
		proxies: &microsoftProxyProviderStub{acquireFn: func(request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
			require.Equal(t, proxydomain.ProxyPurposeBinding, request.Purpose)
			require.Equal(t, proxydomain.ProxyIPv4, request.IPVersion)
			return &proxyapp.ProxyConfig{ID: 31, URL: "socks5://proxy.invalid:1080"}, nil
		}},
		probePasswordRecovery: func(_ context.Context, _ string, proxyURL string, preferredBinding string) (msacl.PasswordRecoveryProbeResult, error) {
			proxy = proxyURL
			preferred = preferredBinding
			return eligibleRecoveryProbe(), nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			return msacl.BindingRecoveryEligibility{Allowed: true}
		},
	}
	snapshot := &maildomain.MicrosoftBindingMailbox{
		ID:             23,
		ResourceID:     2,
		BindingAddress: "wrong@legacy.test",
		Status:         maildomain.MicrosoftBindingPending,
		UpdatedAt:      updatedAt,
	}

	candidate, unavailable, err := adapter.recoverBindingForValidation(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   2,
		EmailAddress: "owner@example.test",
		RequestID:    "validation-2",
	}, snapshot)

	require.NoError(t, err)
	require.False(t, unavailable)
	require.NotNil(t, candidate)
	require.Equal(t, "qalpha01@recovery.test", candidate.Address)
	require.Equal(t, uint(23), candidate.ExpectedBindingID)
	require.Equal(t, "wrong@legacy.test", candidate.ExpectedBindingAddress)
	require.Equal(t, updatedAt, candidate.ExpectedBindingUpdatedAt)
	require.Empty(t, preferred, "unverified/generated candidates must never bias proof enumeration")
	require.Equal(t, "socks5://proxy.invalid:1080", proxy)
}

func TestRecoverBindingForValidationSkipsUnreceivableOrExternalProof(t *testing.T) {
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		bindings: &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			return msacl.PasswordRecoveryProbeResult{
				BindingAddress:  "proof@external.test",
				BindingResolved: true,
			}, nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			return msacl.BindingRecoveryEligibility{Reason: msacl.BindingRecoverySkipExternalMailbox}
		},
	}

	candidate, unavailable, err := adapter.recoverBindingForValidation(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   7,
		EmailAddress: "owner@example.test",
	}, nil)
	require.NoError(t, err)
	require.False(t, unavailable)
	require.Nil(t, candidate)
	require.Equal(t, 1, probeCalls)
}

func TestRecoverBindingForValidationSkipsCompleteVerifiedBindingWithoutProbe(t *testing.T) {
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		bindings: &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			return msacl.PasswordRecoveryProbeResult{}, nil
		},
	}

	candidate, unavailable, err := adapter.recoverBindingForValidation(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   8,
		EmailAddress: "owner@example.test",
	}, &maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   "owner@example.test",
		BindingAddress: "proof@recovery.test",
	})
	require.NoError(t, err)
	require.False(t, unavailable)
	require.Nil(t, candidate)
	require.Zero(t, probeCalls)
}

func TestBindingSnapshotUsesConcreteAddressIndependentOfExecutionStatus(t *testing.T) {
	const accountEmail = "owner@example.test"
	require.False(t, bindingSnapshotHasConcreteAddress(nil, accountEmail))
	require.False(t, bindingSnapshotHasConcreteAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   accountEmail,
		BindingAddress: "qa*****@recovery.test",
	}, accountEmail))
	require.True(t, bindingSnapshotHasConcreteAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingPending,
		AccountEmail:   accountEmail,
		BindingAddress: "qa01@recovery.test",
	}, accountEmail))
	require.True(t, bindingSnapshotHasConcreteAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   accountEmail,
		BindingAddress: "qa01@recovery.test",
	}, accountEmail))
	require.False(t, bindingSnapshotHasConcreteAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   "old-owner@example.test",
		BindingAddress: "qa01@recovery.test",
	}, accountEmail))
	require.True(t, bindingSnapshotHasConcreteAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   accountEmail,
		BindingAddress: "qa01@recovery.test",
		CodeMessageID:  "legacy-code-message",
	}, accountEmail))
	require.False(t, bindingSnapshotHasConcreteAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingExpired,
		AccountEmail:   accountEmail,
		BindingAddress: "qa01@recovery.test",
	}, accountEmail))
	require.Empty(t, bindingSnapshotPreferredAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   accountEmail,
		BindingAddress: "qa*****@recovery.test",
	}, accountEmail))
	require.Equal(t, "qa01@recovery.test", bindingSnapshotPreferredAddress(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   accountEmail,
		BindingAddress: " QA01@RECOVERY.test ",
	}, accountEmail))
	require.True(t, shouldProbeBindingRecovery(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   accountEmail,
		BindingAddress: "qa*****@recovery.test",
	}, accountEmail))
	require.False(t, shouldProbeBindingRecovery(&maildomain.MicrosoftBindingMailbox{
		Status:         maildomain.MicrosoftBindingVerified,
		AccountEmail:   accountEmail,
		BindingAddress: "qa01@recovery.test",
	}, accountEmail))
}

func TestPrepareBindingAddressReplacesMaskedSnapshotWithDeterministicCandidate(t *testing.T) {
	adapter := &ResourceValidationAdapter{}
	req := coreapp.MicrosoftValidationRequest{
		EmailAddress: "owner@example.test",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	}
	expected := deterministicBindingAddress(t, req.EmailAddress)

	actual, err := adapter.prepareBindingAddress(req, maskedBindingAddress(t, expected))

	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.NotContains(t, actual, "*")
}

func TestShouldFallbackRefreshTokenOnlyForExplicitExpiredCategories(t *testing.T) {
	for _, category := range []string{"oauth_invalid_grant", "refresh_token_expired", "oauth_refresh_token_expired"} {
		require.True(t, shouldFallbackInvalidRefreshToken(mailinfra.MicrosoftOAuthResult{Category: category}), category)
	}
	for _, category := range []string{"oauth_client", "oauth_permission", "mfa", "passkey", "phone", "password", "request", ""} {
		require.False(t, shouldFallbackInvalidRefreshToken(mailinfra.MicrosoftOAuthResult{
			Category:    category,
			SafeMessage: "refresh token is invalid or expired",
		}), category)
	}
}

func TestBindingObservationPersistsMaskInBindingAddress(t *testing.T) {
	observation := bindingObservationFromOAuthResult(mailinfra.MicrosoftOAuthResult{
		BindingAddress: "QA*****@Recovery.test",
		BindingStatus:  string(maildomain.MicrosoftBindingFailed),
		SafeMessage:    "Microsoft account is already bound.",
	})
	require.Equal(t, &coreapp.MicrosoftBindingObservation{
		Address:     "qa*****@recovery.test",
		Status:      string(maildomain.MicrosoftBindingFailed),
		SafeMessage: "Microsoft account is already bound.",
	}, observation)
}

func TestPreparedBindingObservationUsesOnlyCompleteCandidate(t *testing.T) {
	result := coreapp.MicrosoftValidationResult{BindingObservation: &coreapp.MicrosoftBindingObservation{
		Address: "qa*****@recovery.test",
		Status:  string(maildomain.MicrosoftBindingFailed),
	}}
	ensurePreparedBindingObservation(&result, mailinfra.MicrosoftOAuthResult{}, "qa*****@recovery.test")
	require.Equal(t, "qa*****@recovery.test", result.BindingObservation.Address)

	result.BindingObservation.Address = ""
	ensurePreparedBindingObservation(&result, mailinfra.MicrosoftOAuthResult{}, "qalpha01@recovery.test")
	require.Equal(t, "qalpha01@recovery.test", result.BindingObservation.Address)
}

func TestValidateMicrosoftRTInvalidGrantFallsBackWithIndependentBindingProxy(t *testing.T) {
	proxies := purposeProxyStub()
	oauth := &microsoftOAuthProtocolStub{
		result: mailinfra.MicrosoftOAuthResult{
			Category:    "oauth_invalid_grant",
			SafeMessage: "Microsoft refresh token is invalid or expired.",
		},
		acquireResult: verifiedOAuthResult("fallback-client", "fallback-refresh", "fallback-access", "fallback@recovery.test"),
	}
	fetcher := successfulMicrosoftFetcher()
	adapter := &ResourceValidationAdapter{
		proxies:               proxies,
		microsoft:             oauth,
		fetcher:               fetcher,
		bindings:              &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: neverProbe(t),
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   101,
		OwnerUserID:  9,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "expired-client",
		RefreshToken: "expired-refresh",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "fallback-client", result.ClientID)
	require.Equal(t, "fallback-refresh", result.RefreshToken)
	require.Equal(t, 1, oauth.calls)
	require.Equal(t, 1, oauth.acquireCalls)
	require.Equal(t, "socks5://binding-1.invalid:1080", oauth.acquireRequest.ProxyURL)
	require.Equal(t, "fallback-client", fetcher.request.ClientID)
	require.Equal(t, "fallback-refresh", fetcher.request.RefreshToken)
	require.Equal(t, "fallback-access", fetcher.request.AccessToken)
	require.Equal(t, proxydomain.ProxyPurposeAuth, proxies.requests[0].Purpose)
	require.Equal(t, proxydomain.ProxyPurposeBinding, proxies.requests[1].Purpose)
}

func TestValidateMicrosoftStructuredInvalidGrantFallsBackEvenWithDiagnosticError(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{
		result: mailinfra.MicrosoftOAuthResult{
			Category:    "oauth_invalid_grant",
			SafeMessage: "Microsoft refresh token is invalid or expired.",
		},
		err:           errors.New("oauth endpoint returned invalid_grant"),
		acquireResult: verifiedOAuthResult("fallback-client", "fallback-refresh", "fallback-access", "fallback@recovery.test"),
	}
	adapter := &ResourceValidationAdapter{
		microsoft:             oauth,
		fetcher:               successfulMicrosoftFetcher(),
		bindings:              &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: neverProbe(t),
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   1011,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "expired-client",
		RefreshToken: "expired-refresh",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "fallback-client", result.ClientID)
	require.Equal(t, "fallback-refresh", result.RefreshToken)
	require.Equal(t, 1, oauth.acquireCalls)
}

func TestValidateMicrosoftRTInvalidGrantPasswordFallbackFailureOverridesInvalidGrant(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{
		result: mailinfra.MicrosoftOAuthResult{
			Category:    "oauth_invalid_grant",
			SafeMessage: "Microsoft refresh token is invalid or expired.",
		},
		acquireResult: mailinfra.MicrosoftOAuthResult{
			Category:    "password",
			SafeMessage: "Microsoft account password is incorrect.",
		},
	}
	fetcher := &microsoftMailFetcherStub{}
	adapter := &ResourceValidationAdapter{
		microsoft:             oauth,
		fetcher:               fetcher,
		bindings:              &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: neverProbe(t),
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   102,
		EmailAddress: "owner@example.test",
		Password:     "wrong-password",
		ClientID:     "expired-client",
		RefreshToken: "expired-refresh",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.False(t, result.CredentialsAuthoritative)
	require.Equal(t, "password", result.Category)
	require.Equal(t, 1, oauth.acquireCalls)
	require.Zero(t, fetcher.calls)
}

func TestValidateMicrosoftAuthenticationFailureDoesNotDowngradeTrustedBinding(t *testing.T) {
	for _, test := range []struct {
		name         string
		refresh      mailinfra.MicrosoftOAuthResult
		clientID     string
		refreshToken string
	}{
		{name: "password only"},
		{
			name: "invalid refresh fallback",
			refresh: mailinfra.MicrosoftOAuthResult{
				Category:    "oauth_invalid_grant",
				SafeMessage: "Microsoft refresh token is invalid or expired.",
			},
			clientID:     "expired-client",
			refreshToken: "expired-refresh",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			oauth := &microsoftOAuthProtocolStub{
				result: test.refresh,
				acquireResult: mailinfra.MicrosoftOAuthResult{
					Category:    "password",
					SafeMessage: "Microsoft account password is incorrect.",
				},
			}
			adapter := &ResourceValidationAdapter{
				microsoft: oauth,
				fetcher:   &microsoftMailFetcherStub{},
				bindings: &microsoftValidationBindingStoreStub{binding: &maildomain.MicrosoftBindingMailbox{
					ID:             600,
					ResourceID:     600,
					AccountEmail:   "owner@example.test",
					BindingAddress: "trusted@recovery.test",
					Status:         maildomain.MicrosoftBindingVerified,
				}},
			}

			result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
				ResourceID:   600,
				EmailAddress: "owner@example.test",
				Password:     "wrong-password",
				ClientID:     test.clientID,
				RefreshToken: test.refreshToken,
			})

			require.NoError(t, err)
			require.False(t, result.Valid)
			require.Equal(t, "password", result.Category)
			require.Nil(t, result.BindingObservation,
				"candidate-only authentication failure must leave the trusted verified binding untouched")
		})
	}
}

func TestValidateMicrosoftRTInvalidGrantWithoutPasswordPreservesOriginalFailure(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
		Category:    "oauth_invalid_grant",
		SafeMessage: "Microsoft refresh token is invalid or expired.",
	}}
	fetcher := &microsoftMailFetcherStub{}
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   103,
		EmailAddress: "owner@example.test",
		ClientID:     "expired-client",
		RefreshToken: "expired-refresh",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.False(t, result.CredentialsAuthoritative)
	require.Equal(t, "oauth_invalid_grant", result.Category)
	require.Zero(t, oauth.acquireCalls)
	require.Zero(t, fetcher.calls)
}

func TestValidateMicrosoftDoesNotPasswordFallbackForNonExpiredRTFailures(t *testing.T) {
	for _, category := range []string{"oauth_client", "oauth_permission", "mfa", "passkey", "phone"} {
		t.Run(category, func(t *testing.T) {
			oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
				Category:    category,
				SafeMessage: "fixed upstream failure",
			}}
			adapter := &ResourceValidationAdapter{
				microsoft: oauth,
				fetcher:   &microsoftMailFetcherStub{},
				bindings:  &microsoftValidationBindingStoreStub{},
			}

			result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
				ResourceID:   104,
				EmailAddress: "owner@example.test",
				Password:     "private-password",
				ClientID:     "client-id",
				RefreshToken: "refresh-token",
			})

			require.NoError(t, err)
			require.Equal(t, category, result.Category)
			require.Zero(t, oauth.acquireCalls)
		})
	}
}

func TestValidateMicrosoftRTWithCompleteVerifiedBindingSkipsPasswordAndRecovery(t *testing.T) {
	bindings := &microsoftValidationBindingStoreStub{binding: &maildomain.MicrosoftBindingMailbox{
		ID:             11,
		ResourceID:     105,
		AccountEmail:   "owner@example.test",
		BindingAddress: "verified@recovery.test",
		Status:         maildomain.MicrosoftBindingVerified,
	}}
	oauth := &microsoftOAuthProtocolStub{result: verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", "")}
	fetcher := successfulMicrosoftFetcher()
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  bindings,
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			return msacl.PasswordRecoveryProbeResult{}, nil
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   105,
		EmailAddress: "owner@example.test",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, 1, oauth.calls)
	require.Zero(t, oauth.acquireCalls)
	require.Zero(t, probeCalls)
	require.Equal(t, 1, fetcher.calls)
}

func TestValidateMicrosoftLegacyBindingMetadataDoesNotBlockRTSuccess(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*maildomain.MicrosoftBindingMailbox)
	}{
		{
			name: "stale_account_email",
			mutate: func(binding *maildomain.MicrosoftBindingMailbox) {
				binding.AccountEmail = "old-account@example.test"
			},
		},
		{
			name: "stale_code_message",
			mutate: func(binding *maildomain.MicrosoftBindingMailbox) {
				binding.CodeMessageID = "legacy-code-message"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := &maildomain.MicrosoftBindingMailbox{
				ID:             13,
				ResourceID:     1052,
				AccountEmail:   "owner@example.test",
				BindingAddress: "verified@recovery.test",
				Status:         maildomain.MicrosoftBindingVerified,
			}
			tt.mutate(binding)
			oauth := &microsoftOAuthProtocolStub{
				result:        verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
				acquireResult: verifiedOAuthResult("password-client", "password-refresh", "password-access", "verified@recovery.test"),
			}
			fetcher := successfulMicrosoftFetcher()
			probeCalls := 0
			adapter := &ResourceValidationAdapter{
				microsoft: oauth,
				fetcher:   fetcher,
				bindings:  &microsoftValidationBindingStoreStub{binding: binding},
				probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
					probeCalls++
					return msacl.PasswordRecoveryProbeResult{}, nil
				},
			}

			result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
				ResourceID:   1052,
				EmailAddress: "owner@example.test",
				Password:     "private-password",
				ClientID:     "client-id",
				RefreshToken: "refresh-token",
			})

			require.NoError(t, err)
			require.True(t, result.Valid)
			require.Zero(t, oauth.acquireCalls)
			require.Zero(t, probeCalls)
			require.Nil(t, result.BindingObservation)
		})
	}
}

func TestValidateMicrosoftRTIdentityMismatchKeepsRotatedCredentialsButFailsResource(t *testing.T) {
	bindings := &microsoftValidationBindingStoreStub{binding: &maildomain.MicrosoftBindingMailbox{
		ID:             12,
		ResourceID:     1051,
		AccountEmail:   "configured@example.test",
		BindingAddress: "verified@recovery.test",
		Status:         maildomain.MicrosoftBindingVerified,
	}}
	oauth := &microsoftOAuthProtocolStub{result: verifiedOAuthResult("rotated-client", "rotated-refresh", "rotated-access", "")}
	fetcher := &microsoftMailFetcherStub{result: mailinfra.MicrosoftMailFetchResult{
		Category:    "identity_mismatch",
		SafeMessage: "Microsoft OAuth credentials do not match the configured account.",
	}}
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  bindings,
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   1051,
		EmailAddress: "configured@example.test",
		ClientID:     "old-client",
		RefreshToken: "old-refresh",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "identity_mismatch", result.Category)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "rotated-client", result.ClientID)
	require.Equal(t, "rotated-refresh", result.RefreshToken)
	require.Equal(t, 1, fetcher.calls)
}

func TestValidateMicrosoftRTUnresolvedBindingSkipsSupplementaryPassword(t *testing.T) {
	proxies := purposeProxyStub()
	oauth := &microsoftOAuthProtocolStub{
		result:        verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireResult: verifiedOAuthResult("supplement-client", "supplement-refresh", "supplement-access", "verified@recovery.test"),
	}
	fetcher := successfulMicrosoftFetcher()
	fetcher.result.RefreshToken = "fetch-rotated-refresh"
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		proxies:   proxies,
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			return msacl.PasswordRecoveryProbeResult{}, nil
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   106,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "refresh-client", result.ClientID)
	require.Equal(t, "fetch-rotated-refresh", result.RefreshToken)
	require.Equal(t, "refresh-client", fetcher.request.ClientID)
	require.Equal(t, "refresh-token-2", fetcher.request.RefreshToken)
	require.Equal(t, "refresh-access", fetcher.request.AccessToken)
	require.Zero(t, oauth.acquireCalls)
	require.Zero(t, probeCalls)
	require.Nil(t, result.BindingObservation)
	require.Equal(t, proxydomain.ProxyPurposeAuth, proxies.requests[0].Purpose)
	require.Len(t, proxies.requests, 1)
}

func TestValidateMicrosoftRTUnresolvedBindingWithoutPasswordKeepsResourceNormal(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{result: verifiedOAuthResult("rotated-client", "rotated-refresh", "rotated-access", "")}
	fetcher := successfulMicrosoftFetcher()
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   107,
		EmailAddress: "owner@example.test",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Empty(t, result.Category)
	require.Equal(t, "Microsoft resource validation succeeded.", result.SafeMessage)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "rotated-client", result.ClientID)
	require.Equal(t, "rotated-refresh", result.RefreshToken)
	require.Zero(t, oauth.acquireCalls)
	require.Equal(t, 1, fetcher.calls)
}

func TestValidateMicrosoftRTSupplementaryFailureKeepsResourceNormal(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{
		result: verifiedOAuthResult("rotated-client", "rotated-refresh", "rotated-access", ""),
		acquireResult: mailinfra.MicrosoftOAuthResult{
			Category:    "auth_timeout",
			SafeMessage: "Microsoft authorization timed out.",
		},
	}
	fetcher := successfulMicrosoftFetcher()
	adapter := &ResourceValidationAdapter{
		microsoft:             oauth,
		fetcher:               fetcher,
		bindings:              &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: neverProbe(t),
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   108,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Empty(t, result.Category)
	require.Equal(t, "Microsoft resource validation succeeded.", result.SafeMessage)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "rotated-client", result.ClientID)
	require.Equal(t, "rotated-refresh", result.RefreshToken)
	require.Equal(t, 1, fetcher.calls)
}

func TestValidateMicrosoftRTSuccessDoesNotProbeOrConfirmBinding(t *testing.T) {
	proxies := purposeProxyStub()
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{
		result: verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireFn: func(request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
			acquireCall++
			if acquireCall == 1 {
				return mailinfra.MicrosoftOAuthResult{
					Category:       "already_bound",
					BindingAddress: "qa*****@recovery.test",
					BindingStatus:  string(maildomain.MicrosoftBindingFailed),
					SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
				}, nil
			}
			require.Equal(t, "qalpha01@recovery.test", request.BindingAddress)
			return verifiedOAuthResult("confirm-client", "confirm-refresh", "confirm-access", "qalpha01@recovery.test"), nil
		},
	}
	fetcher := successfulMicrosoftFetcher()
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		proxies:   proxies,
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(_ context.Context, _ string, _ string, preferred string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			require.Empty(t, preferred)
			return eligibleRecoveryProbe(), nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			return msacl.BindingRecoveryEligibility{Allowed: true}
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   109,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "refresh-client", result.ClientID, "RT-valid confirmation must not replace refreshed credentials")
	require.Equal(t, "refresh-token-2", result.RefreshToken)
	require.Nil(t, result.RecoveredBinding)
	require.Nil(t, result.BindingObservation)
	require.Zero(t, oauth.acquireCalls)
	require.Zero(t, probeCalls)
	require.Equal(t, "refresh-client", fetcher.request.ClientID)
}

func TestValidateMicrosoftRTSuccessDoesNotEnterRecoveryConfirmation(t *testing.T) {
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{
		result: verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireFn: func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
			acquireCall++
			if acquireCall == 1 {
				return mailinfra.MicrosoftOAuthResult{
					Category:       "already_bound",
					BindingAddress: "qa*****@recovery.test",
					SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
				}, nil
			}
			return mailinfra.MicrosoftOAuthResult{
				Category:       "code_timeout",
				BindingAddress: "qalpha01@recovery.test",
				BindingStatus:  string(maildomain.MicrosoftBindingTimeout),
				SafeMessage:    "Auxiliary mailbox verification code was not received in time.",
			}, nil
		},
	}
	fetcher := successfulMicrosoftFetcher()
	adapter := recoveryEnabledAdapter(oauth, fetcher)

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   110,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Empty(t, result.Category)
	require.Equal(t, "Microsoft resource validation succeeded.", result.SafeMessage)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "refresh-client", result.ClientID)
	require.Nil(t, result.RecoveredBinding)
	require.Nil(t, result.BindingObservation)
	require.Zero(t, oauth.acquireCalls)
	require.Equal(t, 1, fetcher.calls)
}

func TestValidateMicrosoftRTSuccessDoesNotProbeExternalMask(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{
		result: verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireResult: mailinfra.MicrosoftOAuthResult{
			Category:       "already_bound",
			BindingAddress: "a****b@external.test",
			BindingStatus:  string(maildomain.MicrosoftBindingFailed),
			SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
		},
	}
	fetcher := successfulMicrosoftFetcher()
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			return msacl.PasswordRecoveryProbeResult{BindingAddress: "proof@external.test"}, nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			return msacl.BindingRecoveryEligibility{Reason: msacl.BindingRecoverySkipExternalMailbox}
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   111,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Nil(t, result.RecoveredBinding)
	require.Nil(t, result.BindingObservation)
	require.Zero(t, oauth.acquireCalls)
}

func TestValidateMicrosoftRTSuccessDoesNotProbeMaskedSnapshot(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{
		result: verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireResult: mailinfra.MicrosoftOAuthResult{
			Category:       "already_bound",
			BindingAddress: "qa*****@recovery.test",
			BindingStatus:  string(maildomain.MicrosoftBindingFailed),
			SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
		},
	}
	fetcher := successfulMicrosoftFetcher()
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			return msacl.PasswordRecoveryProbeResult{MaskedBindingAddress: "qa*****@recovery.test"}, nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			return msacl.BindingRecoveryEligibility{Reason: msacl.BindingRecoverySkipMailboxUnreadable}
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   112,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Empty(t, result.Category)
	require.Equal(t, "Microsoft resource validation succeeded.", result.SafeMessage)
	require.True(t, result.CredentialsAuthoritative)
	require.Nil(t, result.RecoveredBinding)
	require.Nil(t, result.BindingObservation)
	require.Zero(t, oauth.acquireCalls)
}

func TestValidateMicrosoftNonRTFullBindingUsesPasswordCredentialsWithoutProbe(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{acquireResult: verifiedOAuthResult(
		"password-client",
		"password-refresh",
		"password-access",
		"full@recovery.test",
	)}
	fetcher := successfulMicrosoftFetcher()
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			return msacl.PasswordRecoveryProbeResult{}, nil
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   113,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "password-client", fetcher.request.ClientID)
	require.Equal(t, "password-refresh", fetcher.request.RefreshToken)
	require.Equal(t, "password-access", fetcher.request.AccessToken)
	require.Zero(t, oauth.calls)
	require.Equal(t, 1, oauth.acquireCalls)
	require.Zero(t, probeCalls)
}

func TestValidateMicrosoftPasswordFlowsCannotVerifyCandidateWithoutProtocolAddress(t *testing.T) {
	tests := []struct {
		name             string
		request          coreapp.MicrosoftValidationRequest
		refreshResult    mailinfra.MicrosoftOAuthResult
		wantClientID     string
		wantRefreshToken string
		wantValid        bool
		wantObservation  bool
	}{
		{
			name: "non_rt",
			request: coreapp.MicrosoftValidationRequest{
				ResourceID: 1131, EmailAddress: "owner@example.test", Password: "private-password",
			},
			wantClientID:     "password-client",
			wantRefreshToken: "password-refresh",
			wantValid:        true,
			wantObservation:  true,
		},
		{
			name: "invalid_rt_fallback",
			request: coreapp.MicrosoftValidationRequest{
				ResourceID: 1132, EmailAddress: "owner@example.test", Password: "private-password",
				ClientID: "expired-client", RefreshToken: "expired-refresh",
			},
			refreshResult: mailinfra.MicrosoftOAuthResult{
				Category: "oauth_invalid_grant", SafeMessage: "Microsoft refresh token is invalid or expired.",
			},
			wantClientID:     "password-client",
			wantRefreshToken: "password-refresh",
			wantValid:        true,
			wantObservation:  true,
		},
		{
			name: "valid_rt_supplementary",
			request: coreapp.MicrosoftValidationRequest{
				ResourceID: 1133, EmailAddress: "owner@example.test", Password: "private-password",
				ClientID: "refresh-client-old", RefreshToken: "refresh-token-old",
			},
			refreshResult:    verifiedOAuthResult("refresh-client", "refresh-token-new", "refresh-access", ""),
			wantClientID:     "refresh-client",
			wantRefreshToken: "refresh-token-new",
			wantValid:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oauth := &microsoftOAuthProtocolStub{
				result: tt.refreshResult,
				acquireResult: mailinfra.MicrosoftOAuthResult{
					Valid:          true,
					ClientID:       "password-client",
					RefreshToken:   "password-refresh",
					AccessToken:    "password-access",
					BindingStatus:  string(maildomain.MicrosoftBindingVerified),
					BindingAddress: "",
				},
			}
			fetcher := successfulMicrosoftFetcher()
			adapter := &ResourceValidationAdapter{
				microsoft:             oauth,
				fetcher:               fetcher,
				bindings:              &microsoftValidationBindingStoreStub{},
				probePasswordRecovery: neverProbe(t),
			}

			result, err := adapter.ValidateMicrosoft(context.Background(), tt.request)

			require.NoError(t, err)
			require.Equal(t, tt.wantValid, result.Valid)
			require.Empty(t, result.Category)
			require.Equal(t, "Microsoft resource validation succeeded.", result.SafeMessage)
			require.True(t, result.CredentialsAuthoritative)
			require.Equal(t, tt.wantClientID, result.ClientID)
			require.Equal(t, tt.wantRefreshToken, result.RefreshToken)
			require.Nil(t, result.RecoveredBinding)
			if tt.wantObservation {
				require.NotNil(t, result.BindingObservation)
				require.Equal(t, string(maildomain.MicrosoftBindingPending), result.BindingObservation.Status)
				require.NotEmpty(t, result.BindingObservation.Address)
			} else {
				require.Nil(t, result.BindingObservation)
			}
			require.Equal(t, 1, fetcher.calls)
		})
	}
}

func TestValidateMicrosoftPasswordFlowRequiresRefreshTokenBeforeMailFetch(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{acquireResult: mailinfra.MicrosoftOAuthResult{
		Valid: true, ClientID: "password-client", AccessToken: "temporary-access-token",
	}}
	fetcher := successfulMicrosoftFetcher()
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID: 1134, EmailAddress: "owner@example.test", Password: "private-password",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Zero(t, fetcher.calls, "an access token without an RT is not a usable validation result")
}

func TestValidateMicrosoftFailedPasswordFlowsCannotEmitVerifiedBinding(t *testing.T) {
	tests := []struct {
		name                string
		request             coreapp.MicrosoftValidationRequest
		refreshResult       mailinfra.MicrosoftOAuthResult
		wantAuthoritative   bool
		wantAuthoritativeRT string
		wantAuthoritativeID string
		wantValid           bool
	}{
		{
			name: "non_rt",
			request: coreapp.MicrosoftValidationRequest{
				ResourceID: 1134, EmailAddress: "owner@example.test", Password: "private-password",
			},
		},
		{
			name: "invalid_rt_fallback",
			request: coreapp.MicrosoftValidationRequest{
				ResourceID: 1135, EmailAddress: "owner@example.test", Password: "private-password",
				ClientID: "expired-client", RefreshToken: "expired-refresh",
			},
			refreshResult: mailinfra.MicrosoftOAuthResult{
				Category: "oauth_invalid_grant", SafeMessage: "Microsoft refresh token is invalid or expired.",
			},
		},
		{
			name: "valid_rt_supplementary",
			request: coreapp.MicrosoftValidationRequest{
				ResourceID: 1136, EmailAddress: "owner@example.test", Password: "private-password",
				ClientID: "refresh-client-old", RefreshToken: "refresh-token-old",
			},
			refreshResult:       verifiedOAuthResult("refresh-client", "refresh-token-new", "refresh-access", ""),
			wantAuthoritative:   true,
			wantAuthoritativeID: "refresh-client",
			wantAuthoritativeRT: "refresh-token-new",
			wantValid:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oauth := &microsoftOAuthProtocolStub{
				result: tt.refreshResult,
				acquireResult: mailinfra.MicrosoftOAuthResult{
					Valid:          false,
					ClientID:       "untrusted-client",
					RefreshToken:   "untrusted-refresh",
					BindingAddress: "candidate@recovery.test",
					BindingStatus:  string(maildomain.MicrosoftBindingVerified),
					Category:       "password",
					SafeMessage:    "Microsoft account password is incorrect.",
				},
			}
			fetcher := &microsoftMailFetcherStub{}
			if tt.wantValid {
				fetcher = successfulMicrosoftFetcher()
			}
			adapter := &ResourceValidationAdapter{
				microsoft:             oauth,
				fetcher:               fetcher,
				bindings:              &microsoftValidationBindingStoreStub{},
				probePasswordRecovery: neverProbe(t),
			}

			result, err := adapter.ValidateMicrosoft(context.Background(), tt.request)

			require.NoError(t, err)
			require.Equal(t, tt.wantValid, result.Valid)
			if tt.wantValid {
				require.Empty(t, result.Category)
				require.Equal(t, "Microsoft resource validation succeeded.", result.SafeMessage)
			} else {
				require.Equal(t, "password", result.Category)
			}
			require.Equal(t, tt.wantAuthoritative, result.CredentialsAuthoritative)
			if tt.wantAuthoritative {
				require.Equal(t, tt.wantAuthoritativeID, result.ClientID)
				require.Equal(t, tt.wantAuthoritativeRT, result.RefreshToken)
			}
			require.Nil(t, result.RecoveredBinding)
			if tt.wantValid {
				require.Nil(t, result.BindingObservation)
				require.Equal(t, 1, fetcher.calls)
			} else {
				require.NotNil(t, result.BindingObservation)
				require.Equal(t, "candidate@recovery.test", result.BindingObservation.Address)
				require.Equal(t, string(maildomain.MicrosoftBindingPending), result.BindingObservation.Status)
				require.Zero(t, fetcher.calls)
			}
		})
	}
}

func TestValidateMicrosoftNonRTAlreadyBoundConfirmsCandidateThenUsesNewCredentials(t *testing.T) {
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{acquireFn: func(request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
		acquireCall++
		if acquireCall == 1 {
			return mailinfra.MicrosoftOAuthResult{
				Category:       "already_bound",
				BindingAddress: "qa*****@recovery.test",
				SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
			}, nil
		}
		require.Equal(t, "qalpha01@recovery.test", request.BindingAddress)
		return verifiedOAuthResult("confirmed-client", "confirmed-refresh", "confirmed-access", "qalpha01@recovery.test"), nil
	}}
	fetcher := successfulMicrosoftFetcher()
	adapter := recoveryEnabledAdapter(oauth, fetcher)

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   114,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "confirmed-client", result.ClientID)
	require.Equal(t, "confirmed-refresh", result.RefreshToken)
	require.NotNil(t, result.RecoveredBinding)
	require.Nil(t, result.BindingObservation)
	require.Equal(t, 2, oauth.acquireCalls)
	require.Equal(t, "confirmed-client", fetcher.request.ClientID)
}

func TestValidateMicrosoftRTInvalidGrantAlreadyBoundConfirmsCandidateThenUsesPasswordCredentials(t *testing.T) {
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{
		result: mailinfra.MicrosoftOAuthResult{
			Category:    "oauth_invalid_grant",
			SafeMessage: "Microsoft refresh token is invalid or expired.",
		},
		acquireFn: func(request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
			acquireCall++
			if acquireCall == 1 {
				return mailinfra.MicrosoftOAuthResult{
					Category:       "already_bound",
					BindingAddress: "qa*****@recovery.test",
					SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
				}, nil
			}
			require.Equal(t, "qalpha01@recovery.test", request.BindingAddress)
			return verifiedOAuthResult("confirmed-client", "confirmed-refresh", "confirmed-access", "qalpha01@recovery.test"), nil
		},
	}
	fetcher := successfulMicrosoftFetcher()
	adapter := recoveryEnabledAdapter(oauth, fetcher)

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   115,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "expired-client",
		RefreshToken: "expired-refresh",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "confirmed-client", result.ClientID)
	require.Equal(t, "confirmed-refresh", result.RefreshToken)
	require.NotNil(t, result.RecoveredBinding)
	require.Equal(t, 2, oauth.acquireCalls)
	require.Equal(t, "confirmed-access", fetcher.request.AccessToken)
}

func TestValidateMicrosoftRecoveryConfirmationTemporaryFailureKeepsCandidateUnverified(t *testing.T) {
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{acquireFn: func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
		acquireCall++
		if acquireCall == 1 {
			return mailinfra.MicrosoftOAuthResult{
				Category:       "already_bound",
				BindingAddress: "qa*****@recovery.test",
				SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
			}, nil
		}
		return mailinfra.MicrosoftOAuthResult{
			Category:       "code_timeout",
			BindingAddress: "qalpha01@recovery.test",
			BindingStatus:  string(maildomain.MicrosoftBindingTimeout),
			SafeMessage:    "Auxiliary mailbox verification code was not received in time.",
		}, nil
	}}
	fetcher := &microsoftMailFetcherStub{}
	adapter := recoveryEnabledAdapter(oauth, fetcher)

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   116,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.False(t, result.CredentialsAuthoritative)
	require.Equal(t, "code_timeout", result.Category)
	require.Nil(t, result.RecoveredBinding)
	require.Equal(t, &coreapp.MicrosoftBindingObservation{
		Address:     "qalpha01@recovery.test",
		Status:      string(maildomain.MicrosoftBindingTimeout),
		SafeMessage: "Auxiliary mailbox verification code was not received in time.",
	}, result.BindingObservation)
	require.Nil(t, result.ReleaseRecoveryLease, "a sent but unresolved code mail must keep the mask leased until TTL")
	require.Zero(t, fetcher.calls)
}

func TestValidateMicrosoftSecondAlreadyBoundConfirmationDoesNotLoopOrRecover(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{acquireResult: mailinfra.MicrosoftOAuthResult{
		Category:       "already_bound",
		BindingAddress: "qa*****@recovery.test",
		SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
	}}
	adapter := recoveryEnabledAdapter(oauth, &microsoftMailFetcherStub{})

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   117,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Nil(t, result.RecoveredBinding)
	require.Equal(t, &coreapp.MicrosoftBindingObservation{
		Address:     "qalpha01@recovery.test",
		Status:      string(maildomain.MicrosoftBindingPending),
		SafeMessage: "Microsoft recovery mailbox confirmation did not match the resolved address.",
	}, result.BindingObservation)
	require.Equal(t, 2, oauth.acquireCalls, "confirmation must be attempted once without a second probe loop")
}

func TestValidateMicrosoftRecoveryConfirmationMustReturnSameCompleteAddress(t *testing.T) {
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{acquireFn: func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
		acquireCall++
		if acquireCall == 1 {
			return mailinfra.MicrosoftOAuthResult{
				Category:       "already_bound",
				BindingAddress: "qa*****@recovery.test",
			}, nil
		}
		return verifiedOAuthResult("confirmed-client", "confirmed-refresh", "confirmed-access", "different@recovery.test"), nil
	}}
	adapter := recoveryEnabledAdapter(oauth, &microsoftMailFetcherStub{})

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   118,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.True(t, result.CredentialsAuthoritative, "the password OAuth login itself succeeded even though binding confirmation mismatched")
	require.Nil(t, result.RecoveredBinding)
	require.Equal(t, "qalpha01@recovery.test", result.BindingObservation.Address)
	require.Equal(t, string(maildomain.MicrosoftBindingPending), result.BindingObservation.Status)
}

func TestValidateMicrosoftRecoveryConfirmationCannotInferMissingProtocolAddress(t *testing.T) {
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{acquireFn: func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
		acquireCall++
		if acquireCall == 1 {
			return mailinfra.MicrosoftOAuthResult{
				Category:       "already_bound",
				BindingAddress: "qa*****@recovery.test",
			}, nil
		}
		return mailinfra.MicrosoftOAuthResult{
			Valid:          true,
			ClientID:       "confirmed-client",
			RefreshToken:   "confirmed-refresh",
			AccessToken:    "confirmed-access",
			BindingStatus:  string(maildomain.MicrosoftBindingVerified),
			BindingAddress: "",
		}, nil
	}}
	fetcher := &microsoftMailFetcherStub{}
	adapter := recoveryEnabledAdapter(oauth, fetcher)

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   1181,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.True(t, result.CredentialsAuthoritative)
	require.Nil(t, result.RecoveredBinding, "candidate backfill must never masquerade as a protocol-confirmed address")
	require.Equal(t, "qalpha01@recovery.test", result.BindingObservation.Address)
	require.Equal(t, string(maildomain.MicrosoftBindingPending), result.BindingObservation.Status)
	require.Zero(t, fetcher.calls)
}

func TestValidateMicrosoftExistingConcreteBindingDoesNotBlockRTSuccess(t *testing.T) {
	bindings := &microsoftValidationBindingStoreStub{binding: &maildomain.MicrosoftBindingMailbox{
		ID:             41,
		ResourceID:     119,
		BindingAddress: "pending@recovery.test",
		Status:         maildomain.MicrosoftBindingFailed,
	}}
	oauth := &microsoftOAuthProtocolStub{
		result:        verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireResult: verifiedOAuthResult("supplement-client", "supplement-refresh", "supplement-access", "pending@recovery.test"),
	}
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   successfulMicrosoftFetcher(),
		bindings:  bindings,
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			return msacl.PasswordRecoveryProbeResult{}, nil
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   119,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Zero(t, oauth.acquireCalls)
	require.Zero(t, probeCalls)
}

func TestValidateMicrosoftMaskedSnapshotDoesNotStartSupplementaryLoginAfterRTSuccess(t *testing.T) {
	bindings := &microsoftValidationBindingStoreStub{binding: &maildomain.MicrosoftBindingMailbox{
		ID:             42,
		ResourceID:     120,
		BindingAddress: "qa*****@recovery.test",
		Status:         maildomain.MicrosoftBindingFailed,
	}}
	expected := deterministicBindingAddress(t, "owner@example.test")
	oauth := &microsoftOAuthProtocolStub{
		result: verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireFn: func(request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
			require.Equal(t, expected, request.BindingAddress)
			require.NotContains(t, request.BindingAddress, "*")
			return verifiedOAuthResult("supplement-client", "supplement-refresh", "supplement-access", expected), nil
		},
	}
	adapter := &ResourceValidationAdapter{
		microsoft:             oauth,
		fetcher:               successfulMicrosoftFetcher(),
		bindings:              bindings,
		probePasswordRecovery: neverProbe(t),
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   120,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Zero(t, oauth.acquireCalls)
}

func TestValidateMicrosoftRecoveryProbeRotatesBindingProxyWithoutCondemningIt(t *testing.T) {
	proxyCall := 0
	proxies := &microsoftProxyProviderStub{acquireFn: func(request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
		proxyCall++
		return &proxyapp.ProxyConfig{
			ID:  uint(300 + proxyCall),
			URL: fmt.Sprintf("socks5://%s-%d.invalid:1080", request.Purpose, request.Attempt),
		}, nil
	}}
	acquireCall := 0
	oauth := &microsoftOAuthProtocolStub{acquireFn: func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
		acquireCall++
		if acquireCall == 1 {
			return mailinfra.MicrosoftOAuthResult{
				Category:       "already_bound",
				BindingAddress: "qa*****@recovery.test",
			}, nil
		}
		return verifiedOAuthResult("confirmed-client", "confirmed-refresh", "confirmed-access", "qalpha01@recovery.test"), nil
	}}
	fetcher := successfulMicrosoftFetcher()
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		proxies:   proxies,
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(_ context.Context, _ string, _ string, preferred string) (msacl.PasswordRecoveryProbeResult, error) {
			require.Empty(t, preferred)
			probeCalls++
			if probeCalls == 1 {
				return msacl.PasswordRecoveryProbeResult{}, &msacl.AuthError{
					Message: "temporary request failure",
					Status:  msacl.AuthStatusRequestError,
				}
			}
			return eligibleRecoveryProbe(), nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			return msacl.BindingRecoveryEligibility{Allowed: true}
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   121,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		RequestID:    "probe-rotate",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.NotNil(t, result.RecoveredBinding)
	require.Equal(t, 2, probeCalls)
	require.Empty(t, proxies.failures, "ordinary proof-page failures must not mark a shared proxy abnormal")
	require.Equal(t, 2, oauth.acquireCalls)
}

func TestValidateMicrosoftExhaustedTemporaryRecoveryProbeReturnsRetryablePendingObservation(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{acquireResult: mailinfra.MicrosoftOAuthResult{
		Category:       "already_bound",
		BindingAddress: "qa*****@recovery.test",
		SafeMessage:    "Microsoft account is already bound to another recovery mailbox.",
	}}
	probeCalls := 0
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   &microsoftMailFetcherStub{},
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			probeCalls++
			return msacl.PasswordRecoveryProbeResult{}, &msacl.AuthError{
				Message: "temporary proof lookup failure",
				Status:  msacl.AuthStatusRequestError,
			}
		},
	}
	expectedCandidate := deterministicBindingAddress(t, "owner@example.test")

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   1211,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "Microsoft recovery mailbox lookup is temporarily unavailable.", result.SafeMessage)
	require.False(t, result.CredentialsAuthoritative)
	require.Nil(t, result.RecoveredBinding)
	require.Equal(t, &coreapp.MicrosoftBindingObservation{
		Address:     expectedCandidate,
		Status:      string(maildomain.MicrosoftBindingPending),
		SafeMessage: "Microsoft recovery mailbox lookup is temporarily unavailable.",
	}, result.BindingObservation)
	require.Equal(t, maxMicrosoftProxyAttempts+1, probeCalls)
	require.Equal(t, 1, oauth.acquireCalls, "temporary proof lookup failure must not start confirmation")
}

func TestValidateMicrosoftPropagatesRecoveryProbeCancellationAfterNormalFlow(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{acquireResult: mailinfra.MicrosoftOAuthResult{
		Category:       "already_bound",
		BindingAddress: "qa*****@recovery.test",
	}}
	fetcher := &microsoftMailFetcherStub{}
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			return msacl.PasswordRecoveryProbeResult{}, context.Canceled
		},
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   122,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, coreapp.MicrosoftValidationResult{}, result)
	require.Equal(t, 1, oauth.acquireCalls, "normal binding flow must precede the recovery probe")
	require.Zero(t, fetcher.calls)
}

func TestValidateMicrosoftPropagatesCancellationDuringRecoveryEligibility(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	oauth := &microsoftOAuthProtocolStub{acquireResult: mailinfra.MicrosoftOAuthResult{
		Category:       "already_bound",
		BindingAddress: "qa*****@recovery.test",
	}}
	adapter := &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   &microsoftMailFetcherStub{},
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			return eligibleRecoveryProbe(), nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			cancel()
			return msacl.BindingRecoveryEligibility{Allowed: true}
		},
	}

	_, err := adapter.ValidateMicrosoft(ctx, coreapp.MicrosoftValidationRequest{
		ResourceID:   123,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, oauth.acquireCalls)
}

func TestValidateMicrosoftFetchProxyRetryDoesNotStartBindingFlow(t *testing.T) {
	proxies := purposeProxyStub()
	oauth := &microsoftOAuthProtocolStub{
		result:        verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireResult: verifiedOAuthResult("supplement-client", "supplement-refresh", "supplement-access", "confirmed@recovery.test"),
	}
	fetcher := &microsoftMailFetcherStub{}
	fetcher.fetchFn = func(mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error) {
		if fetcher.calls == 1 {
			return mailinfra.MicrosoftMailFetchResult{}, errors.New("temporary fetch proxy failure")
		}
		return mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph"}, nil
	}
	adapter := &ResourceValidationAdapter{
		proxies:               proxies,
		microsoft:             oauth,
		fetcher:               fetcher,
		bindings:              &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: neverProbe(t),
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   124,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, 2, oauth.calls)
	require.Zero(t, oauth.acquireCalls)
	require.Equal(t, 2, fetcher.calls)
	require.Nil(t, result.BindingObservation)
}

func TestFetchMicrosoftValidationKeepsRotatedTokenReturnedWithError(t *testing.T) {
	adapter := &ResourceValidationAdapter{fetcher: &microsoftMailFetcherStub{
		result: mailinfra.MicrosoftMailFetchResult{RefreshToken: "fetch-rotated-refresh"},
		err:    errors.New("mailbox request failed after token exchange"),
	}}
	base := verifiedOAuthResult("client-id", "old-refresh", "access-token", "verified@recovery.test")

	result, err := adapter.fetchMicrosoftValidation(context.Background(), "owner@example.test", "", base)

	require.Error(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "fetch-rotated-refresh", result.RefreshToken)
}

func TestValidateMicrosoftOuterRetryPreservesLastAuthoritativeRotatedCredentials(t *testing.T) {
	bindings := &microsoftValidationBindingStoreStub{binding: &maildomain.MicrosoftBindingMailbox{
		ID:             52,
		ResourceID:     1241,
		AccountEmail:   "owner@example.test",
		BindingAddress: "verified@recovery.test",
		Status:         maildomain.MicrosoftBindingVerified,
	}}
	refreshCall := 0
	oauth := &microsoftOAuthProtocolStub{refreshFn: func(mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
		refreshCall++
		if refreshCall == 1 {
			return verifiedOAuthResult("rotated-client", "rotated-refresh", "rotated-access", ""), nil
		}
		return mailinfra.MicrosoftOAuthResult{
			Category:    "oauth_client",
			SafeMessage: "Microsoft OAuth client is invalid or not allowed.",
		}, nil
	}}
	fetcher := &microsoftMailFetcherStub{err: errors.New("temporary fetch proxy failure")}
	adapter := &ResourceValidationAdapter{
		proxies:   purposeProxyStub(),
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  bindings,
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   1241,
		EmailAddress: "owner@example.test",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "oauth_client", result.Category)
	require.True(t, result.CredentialsAuthoritative)
	require.Equal(t, "rotated-client", result.ClientID)
	require.Equal(t, "rotated-refresh", result.RefreshToken)
	require.Equal(t, 2, oauth.calls)
	require.Equal(t, 1, fetcher.calls)
}

func TestValidateMicrosoftRTSuccessDoesNotAcquireBindingProxy(t *testing.T) {
	proxies := purposeProxyStub()
	oauth := &microsoftOAuthProtocolStub{
		result: verifiedOAuthResult("refresh-client", "refresh-token-2", "refresh-access", ""),
		acquireResult: mailinfra.MicrosoftOAuthResult{
			Category:     "request",
			SafeMessage:  "Microsoft authorization request failed temporarily.",
			ProxyFailure: true,
		},
	}
	adapter := &ResourceValidationAdapter{
		proxies:               proxies,
		microsoft:             oauth,
		fetcher:               successfulMicrosoftFetcher(),
		bindings:              &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: neverProbe(t),
	}

	result, err := adapter.ValidateMicrosoft(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   125,
		EmailAddress: "owner@example.test",
		Password:     "private-password",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Empty(t, result.Category)
	require.Equal(t, "Microsoft resource validation succeeded.", result.SafeMessage)
	require.True(t, result.CredentialsAuthoritative)
	require.Zero(t, oauth.acquireCalls)
	require.Empty(t, proxies.failures)
	require.Contains(t, proxies.successes, uint(100), "successful RT refresh must settle the auth proxy as healthy")
}

func TestTemporaryMicrosoftRecoveryProbeErrorClassification(t *testing.T) {
	for _, status := range []string{
		msacl.AuthStatusRequestError,
		msacl.AuthStatusAuthTimeout,
		msacl.AuthStatusRateLimited,
	} {
		require.True(t, isTemporaryMicrosoftRecoveryProbeError(&msacl.AuthError{
			Message: "temporary proof lookup failure",
			Status:  status,
		}), status)
	}
	require.False(t, isTemporaryMicrosoftRecoveryProbeError(&msacl.AuthError{
		Message: "unknown mailbox",
		Status:  msacl.AuthStatusUnknownMailbox,
	}))
	require.False(t, isTemporaryMicrosoftRecoveryProbeError(context.Canceled))
}

func purposeProxyStub() *microsoftProxyProviderStub {
	bindingCall := 0
	return &microsoftProxyProviderStub{acquireFn: func(request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
		if request.Purpose == proxydomain.ProxyPurposeAuth {
			return &proxyapp.ProxyConfig{
				ID:  uint(100 + request.Attempt),
				URL: fmt.Sprintf("socks5://auth-%d.invalid:1080", request.Attempt),
			}, nil
		}
		bindingCall++
		return &proxyapp.ProxyConfig{
			ID:  uint(200 + bindingCall),
			URL: fmt.Sprintf("socks5://binding-%d.invalid:1080", bindingCall),
		}, nil
	}}
}

func recoveryEnabledAdapter(oauth *microsoftOAuthProtocolStub, fetcher *microsoftMailFetcherStub) *ResourceValidationAdapter {
	return &ResourceValidationAdapter{
		microsoft: oauth,
		fetcher:   fetcher,
		bindings:  &microsoftValidationBindingStoreStub{},
		probePasswordRecovery: func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
			return eligibleRecoveryProbe(), nil
		},
		evaluateBindingEligibility: func(context.Context, msacl.PasswordRecoveryProbeResult) msacl.BindingRecoveryEligibility {
			return msacl.BindingRecoveryEligibility{Allowed: true}
		},
	}
}

func verifiedOAuthResult(clientID, refreshToken, accessToken, bindingAddress string) mailinfra.MicrosoftOAuthResult {
	result := mailinfra.MicrosoftOAuthResult{
		Valid:        true,
		ClientID:     clientID,
		RefreshToken: refreshToken,
		AccessToken:  accessToken,
	}
	if bindingAddress != "" {
		result.BindingAddress = bindingAddress
		result.BindingStatus = string(maildomain.MicrosoftBindingVerified)
	}
	return result
}

func successfulMicrosoftFetcher() *microsoftMailFetcherStub {
	return &microsoftMailFetcherStub{result: mailinfra.MicrosoftMailFetchResult{
		Valid:    true,
		Protocol: "graph",
	}}
}

func deterministicBindingAddress(t *testing.T, accountEmail string) string {
	t.Helper()
	address, err := msacl.DeterministicAuxiliaryAddress(accountEmail)
	require.NoError(t, err)
	return address
}

func maskedBindingAddress(t *testing.T, address string) string {
	t.Helper()
	local, domain, ok := strings.Cut(address, "@")
	require.True(t, ok)
	require.GreaterOrEqual(t, len(local), 2)
	return local[:1] + "*****" + local[len(local)-1:] + "@" + domain
}

func neverProbe(t *testing.T) func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
	t.Helper()
	return func(context.Context, string, string, string) (msacl.PasswordRecoveryProbeResult, error) {
		t.Fatal("recovery probe must not run")
		return msacl.PasswordRecoveryProbeResult{}, nil
	}
}

func eligibleRecoveryProbe() msacl.PasswordRecoveryProbeResult {
	return msacl.PasswordRecoveryProbeResult{
		Proofs: []msacl.PasswordRecoveryProofInfo{{
			MaskedAddress: "qa*****@recovery.test",
			Type:          "Email",
			Channel:       "Email",
		}},
		MaskedBindingAddress: "qa*****@recovery.test",
		BindingAddress:       "qalpha01@recovery.test",
		BindingResolved:      true,
	}
}
