package msacl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

var proxySchemes = []string{
	"http://",
	"https://",
	"socks5://",
	"socks5h://",
	"socks4://",
	"socks4a://",
}

type Session struct {
	client      sessionHTTPClient
	ctx         context.Context
	navHeaders  map[string]string
	corsHeaders map[string]string
	userAgent   string
	dcInterval  int
	dcExpiresIn int
	usesProxy   bool
}

type sessionTransportError struct {
	error
	usesProxy bool
}

func (e *sessionTransportError) Unwrap() error {
	return e.error
}

func newSessionTransportError(err error, usesProxy bool) error {
	if err == nil {
		return nil
	}
	return &sessionTransportError{error: err, usesProxy: usesProxy}
}

func sessionTransportUsedProxy(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var transportErr *sessionTransportError
	return errors.As(err, &transportErr) && transportErr.usesProxy
}

type sessionHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
	SetFollowRedirect(followRedirect bool)
	GetFollowRedirect() bool
}

type HTTPResponse struct {
	StatusCode int
	Body       string
	URL        string
	Header     http.Header
}

type requestOptions struct {
	Headers           map[string]string
	Data              map[string]string
	JSON              any
	AllowRedirects    bool
	HasAllowRedirects bool
}

func normalizeProxy(proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return ""
	}
	lowered := strings.ToLower(proxy)
	for _, scheme := range proxySchemes {
		if strings.HasPrefix(lowered, scheme) {
			return proxy
		}
	}
	return "http://" + proxy
}

func proxyScheme(proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return "none"
	}
	if !strings.Contains(proxy, "://") {
		return "http(default)"
	}
	return strings.ToLower(strings.SplitN(proxy, "://", 2)[0])
}

func proxyLabel(proxy string) string {
	proxy = normalizeProxy(proxy)
	if proxy == "" {
		return "none"
	}
	parts, err := url.Parse(proxy)
	if err != nil || parts.Hostname() == "" {
		return proxyScheme(proxy)
	}
	port := ""
	if parts.Port() != "" {
		port = ":" + parts.Port()
	}
	return fmt.Sprintf("%s://%s%s", parts.Scheme, parts.Hostname(), port)
}

func newTLSHTTPClient(profile profiles.ClientProfile, proxy string, timeoutSeconds int) (tlsclient.HttpClient, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profile),
		tlsclient.WithTimeoutSeconds(timeoutSeconds),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithDisableHttp3(),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
	}
	if proxy = normalizeProxy(proxy); proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(proxy))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}

func newBrowserSession(ctx context.Context, proxy string) (*Session, error) {
	fp := generateFingerprint()
	proxy = normalizeProxy(proxy)
	if proxy != "" {
		logDebug("微软请求使用代理: %s", proxyLabel(proxy))
	}
	client, err := newTLSHTTPClient(fp.Profile, proxy, 30)
	if err != nil {
		return nil, newSessionTransportError(err, proxy != "")
	}
	return &Session{
		client:      client,
		ctx:         contextOrBackground(ctx),
		navHeaders:  cloneStringMap(fp.HeadersNavigate),
		corsHeaders: cloneStringMap(fp.HeadersCORS),
		userAgent:   fp.UserAgent,
		usesProxy:   proxy != "",
	}, nil
}

func newPlainSession(ctx context.Context, proxy string, timeoutSeconds int) (*Session, error) {
	proxy = normalizeProxy(proxy)
	client, err := newTLSHTTPClient(profiles.Chrome_124, proxy, timeoutSeconds)
	if err != nil {
		return nil, newSessionTransportError(err, proxy != "")
	}
	return &Session{
		client:    client,
		ctx:       contextOrBackground(ctx),
		userAgent: "Mozilla/5.0",
		usesProxy: proxy != "",
	}, nil
}

func (s *Session) Get(rawURL string, opts requestOptions) (*HTTPResponse, error) {
	return s.do(http.MethodGet, rawURL, opts)
}

func (s *Session) Post(rawURL string, opts requestOptions) (*HTTPResponse, error) {
	return s.do(http.MethodPost, rawURL, opts)
}

func (s *Session) Delete(rawURL string, opts requestOptions) (*HTTPResponse, error) {
	return s.do(http.MethodDelete, rawURL, opts)
}

func (s *Session) do(method, rawURL string, opts requestOptions) (*HTTPResponse, error) {
	var body io.Reader
	contentType := ""
	if opts.JSON != nil {
		data, err := json.Marshal(opts.JSON)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
		contentType = "application/json"
	} else if opts.Data != nil {
		values := url.Values{}
		for key, value := range opts.Data {
			values.Set(key, value)
		}
		body = strings.NewReader(values.Encode())
		contentType = "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequestWithContext(s.context(), method, rawURL, body)
	if err != nil {
		return nil, err
	}

	headers := cloneStringMap(opts.Headers)
	if s.userAgent != "" {
		if _, ok := headerLookup(headers, "User-Agent"); !ok {
			headers["User-Agent"] = s.userAgent
		}
	}
	if contentType != "" {
		if _, ok := headerLookup(headers, "Content-Type"); !ok {
			headers["Content-Type"] = contentType
		}
	}
	applyHeaders(req, headers)

	if opts.HasAllowRedirects {
		previous := s.client.GetFollowRedirect()
		s.client.SetFollowRedirect(opts.AllowRedirects)
		defer s.client.SetFollowRedirect(previous)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, newSessionTransportError(err, s.usesProxy)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, newSessionTransportError(err, s.usesProxy)
	}
	respURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		respURL = resp.Request.URL.String()
	}
	return &HTTPResponse{
		StatusCode: resp.StatusCode,
		Body:       string(data),
		URL:        respURL,
		Header:     resp.Header.Clone(),
	}, nil
}

func (s *Session) context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func sleepContext(ctx context.Context, d time.Duration) error {
	ctx = contextOrBackground(ctx)
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) sleep(d time.Duration) error {
	return sleepContext(s.context(), d)
}

func (r *HTTPResponse) JSON(v any) error {
	decoder := json.NewDecoder(strings.NewReader(r.Body))
	decoder.UseNumber()
	return decoder.Decode(v)
}

func applyHeaders(req *http.Request, headers map[string]string) {
	orderedKeys := orderedHeaderKeys(headers)
	for _, key := range orderedKeys {
		value := headers[key]
		req.Header.Set(key, value)
	}
	if len(orderedKeys) > 0 {
		order := make([]string, 0, len(orderedKeys))
		for _, key := range orderedKeys {
			order = append(order, strings.ToLower(key))
		}
		req.Header[http.HeaderOrderKey] = order
	}
}

var browserHeaderOrder = []string{
	"user-agent",
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"sec-ch-ua-platform-version",
	"sec-ch-ua-arch",
	"sec-ch-ua-bitness",
	"upgrade-insecure-requests",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-user",
	"sec-fetch-dest",
	"accept-language",
	"accept",
	"viewport-width",
	"content-type",
	"origin",
	"referer",
	"client-request-id",
	"correlationid",
	"hpgact",
	"hpgid",
	"canary",
	"authorization",
}

func orderedHeaderKeys(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}
	used := map[string]struct{}{}
	keys := make([]string, 0, len(headers))
	add := func(name string) {
		lowerName := strings.ToLower(name)
		if _, ok := used[lowerName]; ok {
			return
		}
		for key := range headers {
			if strings.EqualFold(key, name) {
				keys = append(keys, key)
				used[lowerName] = struct{}{}
				return
			}
		}
	}
	for _, key := range browserHeaderOrder {
		add(key)
	}

	remaining := make([]string, 0, len(headers)-len(keys))
	for key := range headers {
		lowerKey := strings.ToLower(key)
		if _, ok := used[lowerKey]; ok {
			continue
		}
		remaining = append(remaining, key)
	}
	sort.Slice(remaining, func(i, j int) bool {
		return strings.ToLower(remaining[i]) < strings.ToLower(remaining[j])
	})
	keys = append(keys, remaining...)
	return keys
}

func headerLookup(headers map[string]string, key string) (string, bool) {
	for existingKey, value := range headers {
		if strings.EqualFold(existingKey, key) {
			return value, true
		}
	}
	return "", false
}

func mergeHeaders(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for key, value := range m {
			out[key] = value
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func navHeaders(session *Session, extra map[string]string) map[string]string {
	if session == nil {
		return cloneStringMap(extra)
	}
	return mergeHeaders(session.navHeaders, extra)
}

func corsHeaders(session *Session, extra map[string]string) map[string]string {
	if session == nil {
		return cloneStringMap(extra)
	}
	return mergeHeaders(session.corsHeaders, extra)
}

func requestWithRetryGet(session *Session, rawURL, label, boundMailbox string, opts requestOptions) (*HTTPResponse, error) {
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err := session.Get(rawURL, opts)
		if err == nil {
			return resp, nil
		}
		if attempt == 1 && isTransientRequestError(err) {
			logWarning("%s 请求异常, 准备重试: %s", label, err)
			if err := session.sleep(500 * time.Millisecond); err != nil {
				return nil, wrapAuthError(fmt.Sprintf("%s 请求取消: %s", label, err), AuthStatusRequestError, err, boundMailbox)
			}
			continue
		}
		return nil, wrapAuthError(fmt.Sprintf("%s 请求异常: %s", label, err), AuthStatusRequestError, err, boundMailbox)
	}
	return nil, newAuthError(label+" 请求异常", AuthStatusRequestError, boundMailbox)
}

func isTransientRequestError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, marker := range transientRequestMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
