package icloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const appleAccountResponseMaxBytes = 4 << 20

type appleAccountError struct {
	Category    string
	SafeMessage string
	RetryAfter  time.Duration
}

func (e *appleAccountError) Error() string {
	if e == nil || strings.TrimSpace(e.SafeMessage) == "" {
		return "Apple Account request failed."
	}
	return e.SafeMessage
}

type AppleAccountClient struct {
	httpClient *http.Client
}

func NewAppleAccountClient(client *http.Client) *AppleAccountClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if client.CheckRedirect == nil {
		clientCopy := *client
		clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		client = &clientCopy
	}
	return &AppleAccountClient{httpClient: client}
}

func (c *AppleAccountClient) refresh(ctx context.Context, channel iCloudResourceChannelModel, now time.Time) (iCloudResourceChannelModel, error) {
	next := channel
	var token struct {
		TimeOutInterval int `json:"timeOutInterval"`
	}
	if err := c.request(ctx, &next, "", http.MethodGet, "/account/manage/gs/ws/token", nil, &token, now); err != nil {
		return channel, err
	}
	var manage struct {
		APIKey string `json:"apiKey"`
	}
	if err := c.request(ctx, &next, "", http.MethodGet, "/account/manage", nil, &manage, now); err != nil {
		return next, err
	}
	if apiKey := strings.TrimSpace(manage.APIKey); apiKey != "" {
		next.APIKey = apiKey
	}
	if strings.TrimSpace(next.APIKey) == "" {
		return next, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account did not return an API key."}
	}
	if token.TimeOutInterval > 0 {
		expiresAt := now.Add(time.Duration(token.TimeOutInterval) * time.Minute)
		next.ManageExpiresAt = &expiresAt
	}
	return next, nil
}

func (c *AppleAccountClient) create(ctx context.Context, channel iCloudResourceChannelModel, now time.Time) (hmeAlias, iCloudResourceChannelModel, error) {
	if strings.TrimSpace(channel.APIKey) == "" {
		return hmeAlias{}, channel, &appleAccountError{Category: "invalid_context", SafeMessage: "Apple Account API key is missing."}
	}
	next := channel
	var generated struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := c.request(ctx, &next, next.APIKey, http.MethodPost, "/account/manage/email/private/add", map[string]any{}, &generated, now); err != nil {
		return hmeAlias{}, next, err
	}
	generated.EmailAddress = strings.ToLower(strings.TrimSpace(generated.EmailAddress))
	if !validICloudHMEEmail(generated.EmailAddress) {
		return hmeAlias{}, next, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid alias candidate."}
	}
	var completed struct {
		EmailAddress string `json:"emailAddress"`
		Label        string `json:"label"`
		Note         string `json:"note"`
		ID           string `json:"id"`
	}
	if err := c.request(ctx, &next, next.APIKey, http.MethodPut, "/account/manage/email/private/add/complete", map[string]string{
		"emailAddress": generated.EmailAddress,
		"label":        "ReMail",
		"note":         "",
	}, &completed, now); err != nil {
		return hmeAlias{}, next, err
	}
	email := strings.ToLower(strings.TrimSpace(completed.EmailAddress))
	if email == "" {
		email = generated.EmailAddress
	}
	alias := hmeAlias{
		AnonymousID: strings.TrimSpace(completed.ID),
		Email:       email,
		Label:       strings.TrimSpace(completed.Label),
		Note:        strings.TrimSpace(completed.Note),
		Origin:      "APPLE_ACCOUNT",
		Active:      true,
	}
	if alias.AnonymousID != "" && !validICloudHMEText(alias.AnonymousID, iCloudHMEAnonymousIDMaxLength, false) || !validICloudHMEEmail(alias.Email) {
		return hmeAlias{}, next, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid created alias."}
	}
	return alias, next, nil
}

func (c *AppleAccountClient) request(
	ctx context.Context,
	channel *iCloudResourceChannelModel,
	apiKey, method, requestPath string,
	body, result any,
	now time.Time,
) error {
	if channel == nil || (channel.Host != "appleid.apple.com" && channel.Host != "appleid.apple.com.cn") ||
		!validAppleAccountCookie(channel.Cookie) || strings.TrimSpace(channel.Scnt) == "" {
		return &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	endpoint := url.URL{Scheme: "https", Host: channel.Host, Path: requestPath}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	origin := strings.TrimRight(strings.TrimSpace(channel.Origin), "/")
	if origin == "" {
		origin = defaultAppleAccountOrigin(channel.Host)
	}
	userAgent := strings.TrimSpace(channel.UserAgent)
	if userAgent == "" {
		userAgent = defaultICloudHMEUserAgent
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("Referer", origin+"/")
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	request.Header.Set("Cookie", channel.Cookie)
	request.Header.Set("scnt", channel.Scnt)
	request.Header.Set("X-Apple-I-Request-Context", "ca")
	request.Header.Set("X-Apple-I-TimeZone", "Asia/Shanghai")
	request.Header.Set("X-Apple-I-FD-Client-Info", appleAccountFDClientInfo(userAgent))
	request.Header.Set("Sec-Fetch-Site", "same-site")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		request.Header.Set("X-Apple-Api-Key", apiKey)
	}
	client := c
	if client == nil || client.httpClient == nil {
		client = NewAppleAccountClient(nil)
	}
	response, err := client.httpClient.Do(request)
	if err != nil || response == nil || response.Body == nil {
		return &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account is temporarily unavailable."}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, appleAccountResponseMaxBytes+1))
	if readErr != nil || len(data) > appleAccountResponseMaxBytes {
		return &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account returned an unreadable response."}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == 419 {
		return &appleAccountError{Category: "session_invalid", SafeMessage: "Apple Account session is invalid."}
	}
	if response.StatusCode == http.StatusTooManyRequests || appleAccountBodyRateLimited(data) {
		return &appleAccountError{
			Category: "rate_limited", SafeMessage: "Apple Account alias creation is temporarily rate limited.",
			RetryAfter: iCloudRetryAfter(response.Header.Get("Retry-After"), now),
		}
	}
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500 {
		return &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account is temporarily unavailable."}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &appleAccountError{Category: "provider_rejected", SafeMessage: "Apple Account rejected the request."}
	}
	updatedCookie := mergeICloudCookies(channel.Cookie, response.Cookies())
	if !validAppleAccountCookie(updatedCookie) {
		return &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid session cookie."}
	}
	channel.Cookie = updatedCookie
	if value := strings.TrimSpace(response.Header.Get("scnt")); value != "" {
		channel.Scnt = value
	}
	if value := strings.TrimSpace(response.Header.Get("X-Apple-ID-Session-Id")); value != "" {
		channel.SessionID = value
	}
	if value := strings.TrimSpace(response.Header.Get("X-Apple-I-DA-Token")); value != "" {
		channel.DataAccessToken = value
	}
	if result != nil && len(bytes.TrimSpace(data)) > 0 && json.Unmarshal(data, result) != nil {
		return &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid response."}
	}
	return nil
}

func appleAccountBodyRateLimited(body []byte) bool {
	value := strings.ToLower(string(body))
	return strings.Contains(value, "-41015") || strings.Contains(value, "rate limit") ||
		strings.Contains(value, "too many") || strings.Contains(value, "creation limit")
}

func appleAccountFDClientInfo(userAgent string) string {
	encoded, _ := json.Marshal(map[string]string{
		"U": userAgent,
		"L": "zh-CN",
		"Z": "GMT+08:00",
		"V": "1.1",
		"F": "",
	})
	return string(encoded)
}
