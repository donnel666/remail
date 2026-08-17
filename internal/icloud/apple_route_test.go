package icloud

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/stretchr/testify/require"
)

type appleRouteProxyStub struct {
	config   *proxyapp.ProxyConfig
	err      error
	requests []proxyapp.AcquireProxyRequest
	success  []uint
	failure  []uint
}

func (s *appleRouteProxyStub) Acquire(_ context.Context, request proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error) {
	s.requests = append(s.requests, request)
	return s.config, s.err
}

func (s *appleRouteProxyStub) ReportSuccess(_ context.Context, proxyID uint) error {
	s.success = append(s.success, proxyID)
	return nil
}

func (s *appleRouteProxyStub) ReportFailure(_ context.Context, proxyID uint, _ string) error {
	s.failure = append(s.failure, proxyID)
	return nil
}

type appleRouteSessionStub struct {
	response *appleOnboardingHTTPResponse
	err      error
}

func (s *appleRouteSessionStub) Request(string, string, map[string]string, any, bool) (*appleOnboardingHTTPResponse, error) {
	return s.response, s.err
}

func (*appleRouteSessionStub) SnapshotCookies(...string) ([]msacl.SessionCookie, error) {
	return nil, nil
}
func (*appleRouteSessionStub) RestoreCookies([]msacl.SessionCookie) error { return nil }

func TestAppleRouteFailsClosedAndUsesNormalizedStickyKey(t *testing.T) {
	proxies := &appleRouteProxyStub{config: &proxyapp.ProxyConfig{Direct: true}}
	routes := newAppleRouteManager()
	routes.proxies = proxies
	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		t.Fatal("direct proxy configuration must not create an Apple session")
		return nil, nil
	}

	_, err := routes.session(withAppleRouteEmail(context.Background(), "  User@Example.COM "))
	require.ErrorIs(t, err, errAppleProxyUnavailable)
	require.Len(t, proxies.requests, 1)
	require.Equal(t, "user@example.com", proxies.requests[0].Key)
	require.Zero(t, proxies.requests[0].Attempt)
	require.False(t, proxies.requests[0].AllowSystemFallback)
	require.True(t, proxies.requests[0].RenewBinding)

	routes.proxies = nil
	_, err = routes.session(withAppleRouteEmail(context.Background(), "user@example.com"))
	require.ErrorIs(t, err, errAppleProxyUnavailable)
}

func TestAppleRouteOnlyReportsTransportFailures(t *testing.T) {
	proxies := &appleRouteProxyStub{config: &proxyapp.ProxyConfig{ID: 7, URL: "http://proxy.example:8080"}}
	routes := newAppleRouteManager()
	routes.proxies = proxies
	responseStatus := http.StatusTooManyRequests
	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{response: &appleOnboardingHTTPResponse{
			StatusCode: responseStatus,
			Header:     make(http.Header),
		}}, nil
	}
	ctx := withAppleRouteEmail(context.Background(), "user@example.com")

	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusUnauthorized, http.StatusBadRequest} {
		responseStatus = status
		session, err := routes.session(ctx)
		require.NoError(t, err)
		response, err := session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode)
	}
	require.Equal(t, []uint{7, 7, 7, 7}, proxies.success)
	require.Empty(t, proxies.failure)

	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}, nil
	}
	session, err := routes.session(ctx)
	require.NoError(t, err)
	_, err = session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.Error(t, err)
	require.Empty(t, proxies.failure)

	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{err: context.DeadlineExceeded}, nil
	}
	session, err = routes.session(ctx)
	require.NoError(t, err)
	_, err = session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []uint{7}, proxies.failure)

	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return nil, context.DeadlineExceeded
	}
	_, err = routes.session(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []uint{7, 7}, proxies.failure)

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{err: context.DeadlineExceeded}, nil
	}
	session, err = routes.session(canceledCtx)
	require.NoError(t, err)
	_, err = session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []uint{7, 7}, proxies.failure)
}

func TestRoutedAppleOnboardingUsesRequestEmail(t *testing.T) {
	probe := &appleOnboardingContextProbe{}
	provider := &routedAppleOnboardingProvider{delegate: probe}
	_, err := provider.Execute(context.Background(), AppleOnboardingRequest{Email: " User@Example.COM "})
	require.NoError(t, err)
	require.Equal(t, "user@example.com", probe.email)
}

type appleOnboardingContextProbe struct {
	email string
}

func (p *appleOnboardingContextProbe) Execute(ctx context.Context, _ AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	p.email = appleRouteEmail(ctx)
	return AppleOnboardingResponse{}, nil
}
