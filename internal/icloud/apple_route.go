package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

var errAppleProxyUnavailable = errors.New("icloud: Apple proxy unavailable")

type AppleProxyProvider interface {
	Acquire(context.Context, proxyapp.AcquireProxyRequest) (*proxyapp.ProxyConfig, error)
	ReportSuccess(context.Context, uint) error
	ReportFailure(context.Context, uint, string) error
}

type appleHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type appleRouteEmailKey struct{}

type appleRouteManager struct {
	proxies    AppleProxyProvider
	newSession func(context.Context, string) (appleOnboardingHTTPSession, error)
}

func newAppleRouteManager() *appleRouteManager {
	return &appleRouteManager{newSession: func(ctx context.Context, proxyURL string) (appleOnboardingHTTPSession, error) {
		session, err := msacl.NewAppleAPISession(ctx, proxyURL, 30)
		if err != nil {
			return nil, err
		}
		return &appleOnboardingMSACLSession{session: session}, nil
	}}
}

func withAppleRouteEmail(ctx context.Context, email string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, appleRouteEmailKey{}, strings.ToLower(strings.TrimSpace(email)))
}

func appleRouteEmail(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	email, _ := ctx.Value(appleRouteEmailKey{}).(string)
	return strings.ToLower(strings.TrimSpace(email))
}

func (r *appleRouteManager) session(ctx context.Context) (appleOnboardingHTTPSession, error) {
	if r == nil || r.proxies == nil || r.newSession == nil {
		return nil, errAppleProxyUnavailable
	}
	email := appleRouteEmail(ctx)
	if email == "" {
		return nil, errAppleProxyUnavailable
	}
	config, err := r.proxies.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key:                 email,
		IPVersion:           proxydomain.ProxyIPv4,
		Purpose:             proxydomain.ProxyPurposeBinding,
		AllowSystemFallback: false,
		RenewBinding:        true,
		Attempt:             0,
		RequestID:           appleRouteRequestID(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errAppleProxyUnavailable, err)
	}
	if config == nil || config.Direct || config.ID == 0 || strings.TrimSpace(config.URL) == "" {
		return nil, errAppleProxyUnavailable
	}
	session, err := r.newSession(ctx, config.URL)
	if err != nil {
		if isAppleProxyTransportFailure(ctx, err) {
			r.reportFailure(ctx, config.ID)
		}
		return nil, err
	}
	return &appleProxySession{ctx: ctx, inner: session, routes: r, proxyID: config.ID}, nil
}

func (r *appleRouteManager) reportSuccess(ctx context.Context, proxyID uint) {
	if r == nil || r.proxies == nil || proxyID == 0 {
		return
	}
	reportCtx, cancel := appleProxyReportContext(ctx)
	defer cancel()
	_ = r.proxies.ReportSuccess(reportCtx, proxyID)
}

func (r *appleRouteManager) reportFailure(ctx context.Context, proxyID uint) {
	if r == nil || r.proxies == nil || proxyID == 0 {
		return
	}
	reportCtx, cancel := appleProxyReportContext(ctx)
	defer cancel()
	_ = r.proxies.ReportFailure(reportCtx, proxyID, "Apple proxy transport failed.")
}

func appleProxyReportContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func appleRouteRequestID(ctx context.Context) string {
	if ctx != nil {
		if requestID, ok := ctx.Value(platform.RequestIDKey).(string); ok && strings.TrimSpace(requestID) != "" {
			return strings.TrimSpace(requestID)
		}
	}
	return platform.NewUUIDV7String()
}

func isAppleProxyTransportFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ctx == nil || ctx.Err() == nil
	}
	if msacl.IsProxyTransportError(err) {
		return true
	}
	return false
}

type appleProxySession struct {
	ctx     context.Context
	inner   appleOnboardingHTTPSession
	routes  *appleRouteManager
	proxyID uint
}

func (s *appleProxySession) Request(method, rawURL string, headers map[string]string, body any, follow bool) (*appleOnboardingHTTPResponse, error) {
	response, err := s.inner.Request(method, rawURL, headers, body, follow)
	if err != nil {
		if isAppleProxyTransportFailure(s.ctx, err) {
			s.routes.reportFailure(s.ctx, s.proxyID)
		}
		return nil, err
	}
	if response == nil {
		return nil, io.ErrUnexpectedEOF
	}
	s.routes.reportSuccess(s.ctx, s.proxyID)
	return response, nil
}

func (s *appleProxySession) SnapshotCookies(rawURLs ...string) ([]msacl.SessionCookie, error) {
	return s.inner.SnapshotCookies(rawURLs...)
}

func (s *appleProxySession) RestoreCookies(cookies []msacl.SessionCookie) error {
	return s.inner.RestoreCookies(cookies)
}

type appleProxyHTTPClient struct {
	routes *appleRouteManager
}

func (c *appleProxyHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || c == nil || c.routes == nil {
		return nil, errAppleProxyUnavailable
	}
	var body any
	if request.Body != nil {
		data, err := io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > 0 {
			body = json.RawMessage(data)
		}
	}
	session, err := c.routes.session(request.Context())
	if err != nil {
		return nil, err
	}
	response, err := session.Request(request.Method, request.URL.String(), appleRequestHeaders(request.Header), body, false)
	if err != nil {
		return nil, err
	}
	responseURL, parseErr := url.Parse(response.URL)
	if parseErr != nil {
		responseURL = request.URL
	}
	responseRequest := request.Clone(request.Context())
	responseRequest.URL = responseURL
	return &http.Response{
		StatusCode: response.StatusCode,
		Status:     fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode)),
		Header:     response.Header.Clone(),
		Body:       io.NopCloser(strings.NewReader(response.Body)),
		Request:    responseRequest,
	}, nil
}

func appleRequestHeaders(source http.Header) map[string]string {
	headers := make(map[string]string, len(source))
	for key, values := range source {
		separator := ", "
		if strings.EqualFold(key, "Cookie") {
			separator = "; "
		}
		headers[key] = strings.Join(values, separator)
	}
	return headers
}

type routedAppleOnboardingProvider struct {
	delegate AppleOnboardingProvider
}

func (p *routedAppleOnboardingProvider) Execute(ctx context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if p == nil || p.delegate == nil {
		return AppleOnboardingResponse{}, ErrICloudOnboardingProvider
	}
	return p.delegate.Execute(withAppleRouteEmail(ctx, request.Email), request)
}

func newRoutedAppleOnboardingProvider(routes *appleRouteManager) AppleOnboardingProvider {
	return &routedAppleOnboardingProvider{delegate: &appleOnboardingClient{
		newSession: routes.session,
		endpoints:  defaultAppleOnboardingEndpoints(),
		now:        time.Now,
	}}
}

// NewAppleOnboardingClientWithProxyProvider reuses the production sticky
// Apple proxy route for standalone validation tools.
func NewAppleOnboardingClientWithProxyProvider(provider AppleProxyProvider) AppleOnboardingProvider {
	routes := newAppleRouteManager()
	routes.proxies = provider
	return newRoutedAppleOnboardingProvider(routes)
}

func newRoutedAppleAccountClient(routes *appleRouteManager) *AppleAccountClient {
	return &AppleAccountClient{httpClient: &appleProxyHTTPClient{routes: routes}}
}

func newRoutedHMEClient(routes *appleRouteManager) *HMEClient {
	return &HMEClient{httpClient: &appleProxyHTTPClient{routes: routes}}
}

func newRoutedICloudFamilyClient(routes *appleRouteManager) *iCloudFamilyClient {
	return &iCloudFamilyClient{
		httpClient: &appleProxyHTTPClient{routes: routes},
		endpoint:   iCloudFamilyMembersEndpoint,
		now:        time.Now,
	}
}
