package kitesim

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	standard "net/http"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

type browserFingerprint struct {
	Profile   profiles.ClientProfile
	Headers   map[string]string
	UserAgent string
}

type browserVariant struct {
	Profile         profiles.ClientProfile
	Version         string
	Platform        string
	PlatformVersion string
	UserAgentOS     string
}

type redirectScope uint8

type redirectScopeContextKey struct{}

const (
	redirectScopeAPI redirectScope = iota + 1
	redirectScopePayment
)

var browserVariants = []browserVariant{
	{profiles.Chrome_120, "120", "Windows", `"10.0.0"`, "Windows NT 10.0; Win64; x64"},
	{profiles.Chrome_124, "124", "Windows", `"15.0.0"`, "Windows NT 10.0; Win64; x64"},
	{profiles.Chrome_131, "131", "Windows", `"15.0.0"`, "Windows NT 10.0; Win64; x64"},
	{profiles.Chrome_124, "124", "macOS", `"14.5.0"`, "Macintosh; Intel Mac OS X 10_15_7"},
	{profiles.Chrome_131, "131", "Linux", `""`, "X11; Linux x86_64"},
}

var browserLanguages = []string{
	"zh-CN,zh;q=0.9,en;q=0.8",
	"en-US,en;q=0.9",
	"en-US,en;q=0.9,zh-CN;q=0.8",
}

var browserHeaderOrder = []string{
	"user-agent",
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"sec-ch-ua-platform-version",
	"accept",
	"accept-language",
	"content-type",
	"origin",
	"referer",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-dest",
	"token",
}

func newBrowserFingerprint() browserFingerprint {
	variant := browserVariants[randomIndex(len(browserVariants))]
	userAgent := fmt.Sprintf(
		"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36",
		variant.UserAgentOS,
		variant.Version,
	)
	return browserFingerprint{
		Profile: variant.Profile,
		Headers: map[string]string{
			"Accept":                     "application/json, text/plain, */*",
			"Accept-Language":            browserLanguages[randomIndex(len(browserLanguages))],
			"Origin":                     "https://h5.kitesim.co",
			"Referer":                    "https://h5.kitesim.co/",
			"Sec-Fetch-Dest":             "empty",
			"Sec-Fetch-Mode":             "cors",
			"Sec-Fetch-Site":             "same-site",
			"sec-ch-ua":                  chromeSecCHUA(variant.Version),
			"sec-ch-ua-mobile":           "?0",
			"sec-ch-ua-platform":         fmt.Sprintf(`"%s"`, variant.Platform),
			"sec-ch-ua-platform-version": variant.PlatformVersion,
		},
		UserAgent: userAgent,
	}
}

func chromeSecCHUA(version string) string {
	grease := `"Not)A;Brand";v="24"`
	if version == "120" || version == "124" {
		grease = `"Not_A Brand";v="8"`
	}
	return fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", %s`, version, version, grease)
}

func randomIndex(limit int) int {
	if limit <= 1 {
		return 0
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return int(binary.LittleEndian.Uint64(raw[:]) % uint64(limit))
	}
	return int(time.Now().UnixNano() % int64(limit))
}

func newFingerprintHTTPDoer(proxyURL string, fingerprint browserFingerprint) (requestDoer, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(fingerprint.Profile),
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithDisableHttp3(),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithCustomRedirectFunc(kitesimTLSRedirectPolicy),
	}
	normalizedProxy, err := normalizeProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	if normalizedProxy != "" {
		options = append(options, tlsclient.WithProxyUrl(normalizedProxy))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("kitesim: create fingerprint client: %w", err)
	}
	return &tlsHTTPDoer{client: client}, nil
}

func withKitesimRedirectPolicy(client *standard.Client) *standard.Client {
	clone := *client
	previous := clone.CheckRedirect
	clone.CheckRedirect = func(request *standard.Request, via []*standard.Request) error {
		if len(via) > 0 && !sameRedirectOrigin(via[len(via)-1].URL, request.URL) {
			stripStandardRedirectSecrets(request)
		}
		if err := validateKitesimRedirect(request.URL, standardRedirectURLs(via), request.Method, redirectScopeFrom(request.Context())); err != nil {
			return standard.ErrUseLastResponse
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
	return &clone
}

func kitesimTLSRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) > 0 && !sameRedirectOrigin(via[len(via)-1].URL, request.URL) {
		for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "token"} {
			request.Header.Del(name)
		}
	}
	urls := make([]*url.URL, len(via))
	for i := range via {
		urls[i] = via[i].URL
	}
	if err := validateKitesimRedirect(request.URL, urls, request.Method, redirectScopeFrom(request.Context())); err != nil {
		return http.ErrUseLastResponse
	}
	return nil
}

func validateKitesimRedirect(target *url.URL, via []*url.URL, method string, scope redirectScope) error {
	if target == nil || len(via) == 0 || len(via) >= 10 || target.User != nil || target.Hostname() == "" {
		return errors.New("kitesim: invalid redirect")
	}
	initial, previous := via[0], via[len(via)-1]
	if initial == nil || previous == nil || target.Scheme != previous.Scheme || target.Scheme != "http" && target.Scheme != "https" {
		return errors.New("kitesim: unsafe redirect scheme")
	}
	if sameRedirectOrigin(previous, target) {
		return nil
	}
	if scope != redirectScopePayment || method != standard.MethodGet && method != standard.MethodHead ||
		!paymentRedirectHost(initial.Hostname()) || !paymentRedirectHost(target.Hostname()) {
		return errors.New("kitesim: cross-origin redirect blocked")
	}
	return nil
}

func sameRedirectOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func paymentRedirectHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "international.storepay.cn", "oats.allinpay.com", "h5.kitesim.co", "api.kitesim.co",
		"geo.cardinalcommerce.com", "centinelapi.cardinalcommerce.com", "secure5.arcot.com", "h.online-metrix.net":
		return true
	default:
		return false
	}
}

func redirectScopeFrom(ctx context.Context) redirectScope {
	scope, _ := ctx.Value(redirectScopeContextKey{}).(redirectScope)
	return scope
}

func standardRedirectURLs(requests []*standard.Request) []*url.URL {
	urls := make([]*url.URL, len(requests))
	for i := range requests {
		urls[i] = requests[i].URL
	}
	return urls
}

func stripStandardRedirectSecrets(request *standard.Request) {
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "token"} {
		request.Header.Del(name)
	}
}

func normalizeProxyURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("kitesim: invalid proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
		return value, nil
	default:
		return "", fmt.Errorf("kitesim: unsupported proxy scheme")
	}
}

type tlsHTTPDoer struct{ client tlsclient.HttpClient }

func (d *tlsHTTPDoer) Do(request *standard.Request) (*standard.Response, error) {
	upstreamRequest, err := http.NewRequestWithContext(
		request.Context(),
		request.Method,
		request.URL.String(),
		request.Body,
	)
	if err != nil {
		return nil, err
	}
	upstreamRequest.Host = request.Host
	upstreamRequest.ContentLength = request.ContentLength
	upstreamRequest.Header = make(http.Header, len(request.Header))
	for key, values := range request.Header {
		upstreamRequest.Header[key] = append([]string(nil), values...)
	}
	response, err := d.client.Do(upstreamRequest)
	if err != nil {
		return nil, err
	}
	headers := make(standard.Header, len(response.Header))
	for key, values := range response.Header {
		headers[key] = append([]string(nil), values...)
	}
	responseRequest := request
	if response.Request != nil && response.Request.URL != nil {
		if finalURL, parseErr := url.Parse(response.Request.URL.String()); parseErr == nil {
			responseRequest = request.Clone(request.Context())
			responseRequest.Method = response.Request.Method
			responseRequest.URL = finalURL
		}
	}
	return &standard.Response{
		Status: response.Status, StatusCode: response.StatusCode,
		Proto: response.Proto, ProtoMajor: response.ProtoMajor, ProtoMinor: response.ProtoMinor,
		Header: headers, Body: response.Body, ContentLength: response.ContentLength,
		Request: responseRequest,
	}, nil
}
