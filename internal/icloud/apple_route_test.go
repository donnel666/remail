package icloud

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/redis/go-redis/v9"
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
	response     *appleOnboardingHTTPResponse
	err          error
	snapshotErr  error
	restoreErr   error
	snapshotData []msacl.SessionCookie
}

func (s *appleRouteSessionStub) Request(string, string, map[string]string, any, bool) (*appleOnboardingHTTPResponse, error) {
	return s.response, s.err
}

func (s *appleRouteSessionStub) SnapshotCookies(...string) ([]msacl.SessionCookie, error) {
	if s.snapshotErr != nil {
		return nil, s.snapshotErr
	}
	return append([]msacl.SessionCookie(nil), s.snapshotData...), nil
}
func (s *appleRouteSessionStub) RestoreCookies([]msacl.SessionCookie) error { return s.restoreErr }

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
	require.Equal(t, []uint{7, 7, 7}, proxies.success)
	require.Equal(t, []uint{7}, proxies.failure)

	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}, nil
	}
	session, err := routes.session(ctx)
	require.NoError(t, err)
	_, err = session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.Error(t, err)
	require.Equal(t, []uint{7}, proxies.failure)

	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{err: context.DeadlineExceeded}, nil
	}
	session, err = routes.session(ctx)
	require.NoError(t, err)
	_, err = session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []uint{7, 7}, proxies.failure)

	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return nil, context.DeadlineExceeded
	}
	_, err = routes.session(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []uint{7, 7, 7}, proxies.failure)

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{err: context.DeadlineExceeded}, nil
	}
	session, err = routes.session(canceledCtx)
	require.NoError(t, err)
	_, err = session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []uint{7, 7, 7}, proxies.failure)
}

func TestAppleRouteRotatesAfterThreeConsecutive5XXResponses(t *testing.T) {
	proxies := &appleRouteProxyStub{config: &proxyapp.ProxyConfig{ID: 7, ProxyServerID: 11, URL: "http://proxy.example:8080"}}
	routes := newAppleRouteManager()
	routes.proxies = proxies
	clock := time.Now().UTC()
	routes.now = func() time.Time { return clock }
	ctx := withAppleRouteEmail(context.Background(), "rotate@example.com")
	for attempt := 0; attempt < 3; attempt++ {
		routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
			return &appleRouteSessionStub{response: &appleOnboardingHTTPResponse{StatusCode: http.StatusInternalServerError, Header: make(http.Header)}}, nil
		}
		session, err := routes.session(ctx)
		require.NoError(t, err)
		response, err := session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
		require.NoError(t, err)
		if attempt < 2 {
			require.False(t, response.ProxyRotationPending)
			require.False(t, response.ProxyRetryExhausted)
		} else {
			require.True(t, response.ProxyRotationPending)
			require.GreaterOrEqual(t, response.ProxyRetryAfter, time.Minute)
			require.LessOrEqual(t, response.ProxyRetryAfter, 5*time.Minute)
			_, err := routes.session(ctx)
			var rotationErr *appleProxyRotationError
			require.ErrorAs(t, err, &rotationErr)
			require.False(t, rotationErr.Exhausted)
			require.Positive(t, rotationErr.RetryAfter)
			require.Len(t, proxies.requests, 3)
			clock = clock.Add(response.ProxyRetryAfter)
		}
	}

	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{response: &appleOnboardingHTTPResponse{StatusCode: http.StatusInternalServerError, Header: make(http.Header)}}, nil
	}
	proxies.config.ProxyServerID = 12
	session, err := routes.session(ctx)
	require.NoError(t, err)
	response, err := session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.NoError(t, err)
	require.True(t, response.ProxyRetryExhausted)
	require.False(t, response.ProxyRotationPending)
	require.Equal(t, 1, proxies.requests[3].Attempt)
	require.Equal(t, []uint{11}, proxies.requests[3].AvoidProxyServerIDs)
	require.Len(t, proxies.failure, 4)
}

func TestAppleRouteSuccessResetsConsecutive5XXState(t *testing.T) {
	routes := newAppleRouteManager()
	ctx := withAppleRouteEmail(context.Background(), "reset@example.com")
	for i := 0; i < 2; i++ {
		_, exhausted, err := routes.recordProxy5xx(ctx, "reset@example.com", "", false, 11)
		require.NoError(t, err)
		require.False(t, exhausted)
	}
	state, exists, err := routes.loadRotation(ctx, "reset@example.com")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, 2, state.Consecutive5xx)
	require.NoError(t, routes.recordProxySuccess(ctx, "reset@example.com", "", false))
	_, exists, err = routes.loadRotation(ctx, "reset@example.com")
	require.NoError(t, err)
	require.False(t, exists)

	for i := 0; i < 2; i++ {
		delay, exhausted, err := routes.recordProxy5xx(ctx, "reset@example.com", "", false, 11)
		require.NoError(t, err)
		require.False(t, exhausted)
		require.Zero(t, delay)
	}
	delay, exhausted, err := routes.recordProxy5xx(ctx, "reset@example.com", "", false, 11)
	require.NoError(t, err)
	require.False(t, exhausted)
	require.GreaterOrEqual(t, delay, time.Minute)
}

func TestAppleRouteSuccessDoesNotCancelPendingRotation(t *testing.T) {
	routes := newAppleRouteManager()
	ctx := withAppleRouteEmail(context.Background(), "pending-success@example.com")
	for i := 0; i < 3; i++ {
		_, _, err := routes.recordProxy5xx(ctx, "pending-success@example.com", "", false, 11)
		require.NoError(t, err)
	}
	require.NoError(t, routes.recordProxySuccess(ctx, "pending-success@example.com", "", false))
	state, exists, err := routes.loadRotation(ctx, "pending-success@example.com")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, appleProxyRotationPending, state.Phase)
}

func TestAppleRouteRotationUsesRedisAcrossManagersAndExpires(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	first := newAppleRouteManager(client)
	second := newAppleRouteManager(client)
	first.proxies = &appleRouteProxyStub{config: &proxyapp.ProxyConfig{ID: 7, ProxyServerID: 11, URL: "http://proxy.example:8080"}}
	second.proxies = first.proxies
	ctx := withAppleRouteEmail(context.Background(), "shared@example.com")
	for i := 0; i < 3; i++ {
		delay, exhausted, err := first.recordProxy5xx(ctx, "shared@example.com", "", false, 11)
		require.NoError(t, err)
		require.False(t, exhausted)
		if i == 2 {
			require.GreaterOrEqual(t, delay, time.Minute)
		}
	}
	state, exists, err := second.loadRotation(ctx, "shared@example.com")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, appleProxyRotationPending, state.Phase)
	require.Positive(t, server.TTL(appleProxyRotationKey("shared@example.com")))
	server.FastForward(appleProxyRotationTTL + time.Second)
	_, exists, err = second.loadRotation(ctx, "shared@example.com")
	require.NoError(t, err)
	require.False(t, exists)
}

func TestAppleRouteRotationSuccessDeletesRedisState(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	routes := newAppleRouteManager(client)
	routes.proxies = &appleRouteProxyStub{config: &proxyapp.ProxyConfig{ID: 7, ProxyServerID: 11, URL: "http://proxy.example:8080"}}
	clock := time.Now().UTC()
	routes.now = func() time.Time { return clock }
	ctx := withAppleRouteEmail(context.Background(), "cleanup@example.com")
	for i := 0; i < 3; i++ {
		_, _, err := routes.recordProxy5xx(ctx, "cleanup@example.com", "", false, 11)
		require.NoError(t, err)
	}
	state, exists, err := routes.loadRotation(ctx, "cleanup@example.com")
	require.NoError(t, err)
	require.True(t, exists)
	clock = state.NotBefore.Add(time.Second)
	rotation, rotating, err := routes.takeRotation(ctx, "cleanup@example.com")
	require.NoError(t, err)
	require.True(t, rotating)
	require.NoError(t, routes.recordProxySuccess(ctx, "cleanup@example.com", rotation.Token, true))
	require.Zero(t, server.Exists(appleProxyRotationKey("cleanup@example.com")))
}

func TestAppleRouteRotatedSnapshotFailureIsTerminal(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	routes := newAppleRouteManager(client)
	proxies := &appleRouteProxyStub{config: &proxyapp.ProxyConfig{ID: 7, ProxyServerID: 12, URL: "http://proxy.example:8080"}}
	routes.proxies = proxies
	clock := time.Now().UTC()
	routes.now = func() time.Time { return clock }
	ctx := withAppleRouteDeferredRotation(withAppleRouteEmail(context.Background(), "snapshot@example.com"))
	for i := 0; i < 3; i++ {
		_, _, err := routes.recordProxy5xx(ctx, "snapshot@example.com", "", false, 11)
		require.NoError(t, err)
	}
	state, exists, err := routes.loadRotation(ctx, "snapshot@example.com")
	require.NoError(t, err)
	require.True(t, exists)
	clock = state.NotBefore.Add(time.Second)
	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{
			response:    &appleOnboardingHTTPResponse{StatusCode: http.StatusOK, Header: make(http.Header)},
			snapshotErr: errors.New("snapshot failed"),
		}, nil
	}
	session, err := routes.session(ctx)
	require.NoError(t, err)
	response, err := session.Request(http.MethodGet, "https://appleid.apple.com/account/manage", nil, nil, false)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_, err = session.SnapshotCookies("https://appleid.apple.com")
	var rotationErr *appleProxyRotationError
	require.ErrorAs(t, err, &rotationErr)
	require.True(t, rotationErr.Exhausted)
	state, exists, err = routes.loadRotation(ctx, "snapshot@example.com")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, appleProxyRotationExhausted, state.Phase)
}

func TestAppleRouteResetReleasesUnstartedRotationClaim(t *testing.T) {
	routes := newAppleRouteManager()
	routes.proxies = &appleRouteProxyStub{config: &proxyapp.ProxyConfig{ID: 7, ProxyServerID: 12, URL: "http://proxy.example:8080"}}
	clock := time.Now().UTC()
	routes.now = func() time.Time { return clock }
	ctx := withAppleRouteDeferredRotation(withAppleRouteEmail(context.Background(), "reset-claim@example.com"))
	for i := 0; i < 3; i++ {
		_, _, err := routes.recordProxy5xx(ctx, "reset-claim@example.com", "", false, 11)
		require.NoError(t, err)
	}
	state, _, err := routes.loadRotation(ctx, "reset-claim@example.com")
	require.NoError(t, err)
	clock = state.NotBefore.Add(time.Second)
	routes.newSession = func(context.Context, string) (appleOnboardingHTTPSession, error) {
		return &appleRouteSessionStub{}, nil
	}
	session, err := routes.session(ctx)
	require.NoError(t, err)
	rotated, ok := session.(*appleProxySession)
	require.True(t, ok)
	require.NoError(t, rotated.resetProxyRotation())
	state, exists, err := routes.loadRotation(ctx, "reset-claim@example.com")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, appleProxyRotationPending, state.Phase)
	_, rotating, err := routes.takeRotation(ctx, "reset-claim@example.com")
	require.NoError(t, err)
	require.True(t, rotating)
}

func TestAppleRouteRotatedFailureIsTerminal(t *testing.T) {
	proxies := &appleRouteProxyStub{config: &proxyapp.ProxyConfig{ID: 7, ProxyServerID: 22, URL: "http://proxy.example:8080"}}
	routes := newAppleRouteManager()
	routes.proxies = proxies
	ctx := withAppleRouteEmail(context.Background(), "terminal@example.com")
	for i := 0; i < 3; i++ {
		_, _, err := routes.recordProxy5xx(ctx, "terminal@example.com", "", false, 11)
		require.NoError(t, err)
	}
	state, _, err := routes.loadRotation(ctx, "terminal@example.com")
	require.NoError(t, err)
	state.NotBefore = time.Now().UTC().Add(-time.Second)
	routes.rotations["terminal@example.com"] = state
	rotation, rotating, err := routes.takeRotation(ctx, "terminal@example.com")
	require.NoError(t, err)
	require.True(t, rotating)
	require.NoError(t, routes.recordProxyTransportFailure(ctx, "terminal@example.com", rotation.Token, true))
	_, _, err = routes.takeRotation(ctx, "terminal@example.com")
	var rotationErr *appleProxyRotationError
	require.ErrorAs(t, err, &rotationErr)
	require.True(t, rotationErr.Exhausted)
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
