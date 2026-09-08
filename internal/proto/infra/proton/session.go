package proton

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const maxMessageBytes = 32 << 20

// protonmail-api-client 2.4.3 uses Other for its direct (DEV) SRP client.
// A server rejection of this version is a protocol error, not a bad password.
const appVersion = "Other"

type Client struct{ baseURL string }

func NewClient() *Client { return &Client{baseURL: "https://mail.proton.me/api"} }

type transport struct {
	client  tlsclient.HttpClient
	baseURL string
	proxy   bool
}

// Independently copied from the Microsoft browser-session transport: Chrome
// fingerprint, cookie jar, disabled HTTP/3, verified TLS and bounded requests.
func (c *Client) transport(proxy string) (*transport, error) {
	proxy = strings.TrimSpace(proxy)
	if proxy != "" && !strings.Contains(proxy, "://") {
		proxy = "http://" + proxy
	}
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profiles.Chrome_124),
		tlsclient.WithTimeoutSeconds(30),
		tlsclient.WithDisableHttp3(),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithNotFollowRedirects(),
	}
	if proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Hostname() == "" {
			return nil, &Failure{Category: "request", SafeMessage: "Proto proxy configuration is invalid.", ProxyFailure: true, Cause: err}
		}
		options = append(options, tlsclient.WithProxyUrl(proxy))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, &Failure{Category: "request", SafeMessage: "Proto HTTP client is unavailable.", Retryable: true, ProxyFailure: proxy != "", Cause: err}
	}
	return &transport{client: client, baseURL: c.baseURL, proxy: proxy != ""}, nil
}

func (t *transport) request(ctx context.Context, session *Session, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return &Failure{Category: "protocol", SafeMessage: "Proto request could not be encoded.", Cause: err}
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, reader)
	if err != nil {
		return &Failure{Category: "protocol", SafeMessage: "Proto request is invalid.", Cause: err}
	}
	req.Header = http.Header{
		"accept":            {"application/vnd.protonmail.v1+json"},
		"accept-language":   {"en-US,en;q=0.5"},
		"content-type":      {"application/json"},
		"origin":            {"https://mail.proton.me"},
		"referer":           {"https://mail.proton.me/"},
		"user-agent":        {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"},
		"x-pm-appversion":   {appVersion},
		"x-pm-apiversion":   {"4"},
		"x-pm-locale":       {"en_US"},
		http.HeaderOrderKey: {"user-agent", "accept", "accept-language", "content-type", "origin", "referer", "x-pm-appversion", "x-pm-apiversion", "x-pm-locale", "x-pm-uid", "authorization"},
	}
	if session != nil {
		req.Header.Set("x-pm-uid", session.UID)
		if session.AccessToken != "" {
			req.Header.Set("authorization", "Bearer "+session.AccessToken)
		}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return &Failure{Category: "request", SafeMessage: "Proto service could not be reached.", Retryable: true, ProxyFailure: t.proxy, Cause: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
	if err != nil {
		return &Failure{Category: "request", SafeMessage: "Proto response could not be read.", Retryable: true, ProxyFailure: t.proxy, Cause: err}
	}
	if len(data) > maxMessageBytes {
		return &Failure{Category: "protocol", SafeMessage: "Proto response exceeds the supported message size."}
	}
	var envelope struct{ Code *int }
	if json.Unmarshal(data, &envelope) != nil || envelope.Code == nil || *envelope.Code <= 0 {
		return &Failure{Category: "protocol", SafeMessage: "Proto returned an invalid API response.", Retryable: true}
	}
	if resp.StatusCode >= 300 || (*envelope.Code != 1000 && *envelope.Code != 1001) {
		return responseFailure(resp.StatusCode, *envelope.Code, path)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return &Failure{Category: "protocol", SafeMessage: "Proto returned unexpected response fields.", Retryable: true}
		}
	}
	return nil
}

func responseFailure(status, code int, path string) *Failure {
	switch {
	case code == 9001 || code == 12087 || code == 10004:
		return &Failure{Category: "action_required", SafeMessage: "Proto requires an additional account verification or account action."}
	// Mail and refresh requests never carry a password, so their errors cannot
	// establish that the stored account password is wrong.
	case path == "/auth/v4" && (code == 8002 || code == 6003),
		path == "/auth/v4/info" && code == 6003:
		return &Failure{Category: "invalid_credentials", SafeMessage: "Proto rejected the account credentials."}
	case (status == 401 && path != "/auth/v4" && path != "/auth/v4/info") || code == 10013 || (path == "/auth/v4/refresh" && (code == 8002 || code == 6003)):
		return &Failure{Category: "session_revoked", SafeMessage: "The Proto session is no longer valid; revalidation is required."}
	case status == 429 || code == 2028:
		return &Failure{Category: "rate_limited", SafeMessage: "Proto temporarily limited account requests.", Retryable: true}
	case code == 5001 || code == 5003:
		return &Failure{Category: "protocol", SafeMessage: "Proto requires a supported client version."}
	default:
		return &Failure{Category: "request", SafeMessage: "Proto temporarily rejected the request.", Retryable: true}
	}
}

func (t *transport) authenticated(ctx context.Context, session *Session, path string, out any, request FetchRequest) error {
	err := t.request(ctx, session, http.MethodGet, path, nil, out)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Category != "session_revoked" {
		return err
	}
	if err := t.refreshForFetch(ctx, session, request); err != nil {
		return err
	}
	return t.request(ctx, session, http.MethodGet, path, nil, out)
}

func (t *transport) refresh(ctx context.Context, session *Session, onSession func(Session) error) error {
	if onSession == nil {
		return &Failure{Category: "session_persistence", SafeMessage: "Proto session refresh requires a session store.", Retryable: true}
	}
	if err := t.refreshTokens(ctx, session); err != nil {
		return err
	}
	if err := onSession(*session); err != nil {
		return &Failure{Category: "session_persistence", SafeMessage: "The refreshed Proto session could not be saved.", Retryable: true, Cause: err}
	}
	return nil
}

func (t *transport) refreshForFetch(ctx context.Context, session *Session, request FetchRequest) error {
	if request.RefreshSession != nil {
		return request.RefreshSession(ctx, session, t.refreshTokens)
	}
	return t.refresh(ctx, session, request.OnSession)
}

// refreshTokens is run by the session store while it owns the refresh lease.
// Only that store may decide whether a newer persisted session avoids rotation.
func (t *transport) refreshTokens(ctx context.Context, session *Session) error {
	if session == nil || session.UID == "" || session.RefreshToken == "" {
		return responseFailure(401, 10013, "/auth/v4/refresh")
	}
	var state [24]byte
	if _, err := rand.Read(state[:]); err != nil {
		return &Failure{Category: "request", SafeMessage: "Proto refresh could not be prepared.", Retryable: true, Cause: err}
	}
	var auth authResponse
	if err := t.request(ctx, session, http.MethodPost, "/auth/v4/refresh", map[string]any{
		"UID": session.UID, "RefreshToken": session.RefreshToken, "ResponseType": "token",
		"GrantType": "refresh_token", "RedirectURI": "https://protonmail.ch", "State": base64.RawURLEncoding.EncodeToString(state[:]),
	}, &auth); err != nil {
		return err
	}
	if auth.AccessToken == "" || auth.RefreshToken == "" || (auth.UID != "" && auth.UID != session.UID) {
		return &Failure{Category: "protocol", SafeMessage: "Proto returned an incomplete refreshed session.", Retryable: true}
	}
	session.AccessToken, session.RefreshToken = auth.AccessToken, auth.RefreshToken
	session.ExpiresAt = tokenExpiry(auth.ExpiresIn)
	return nil
}

func tokenExpiry(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Now().UTC().Add(time.Duration(min(seconds, 365*24*60*60)) * time.Second)
}
