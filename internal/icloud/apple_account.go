package icloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/appleweb"
)

const (
	appleAccountResponseMaxBytes            = 4 << 20
	appleAccountTokenPath                   = "/account/manage/gs/ws/token"
	appleAccountPrivateEmailPath            = "/account/manage/email/private"
	appleAccountPrivateEmailAddPath         = "/account/manage/email/private/add"
	appleAccountPrivateEmailAddCompletePath = "/account/manage/email/private/add/complete"
)

type appleAccountPrivateEmail struct {
	EmailAddress   string `json:"emailAddress"`
	Label          string `json:"label"`
	Note           string `json:"note"`
	ForwardToEmail string `json:"forwardToEmail"`
	ID             string `json:"id"`
	Active         *bool  `json:"active"`
}

type appleAccountError struct {
	Category    string
	SafeMessage string
	RetryAfter  time.Duration
	Stage       string
	HTTPStatus  int
}

func (e *appleAccountError) Error() string {
	if e == nil {
		return "Apple Account request failed."
	}
	message := strings.TrimSpace(e.SafeMessage)
	if message == "" {
		message = "Apple Account request failed."
	}
	if e.Stage != "" && e.HTTPStatus > 0 {
		return fmt.Sprintf("%s (stage=%s, HTTP %d)", message, e.Stage, e.HTTPStatus)
	}
	if e.Stage != "" {
		return fmt.Sprintf("%s (stage=%s)", message, e.Stage)
	}
	return message
}

type AppleAccountClient struct {
	httpClient appleHTTPDoer
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
	_, err := c.request(ctx, &next, "", http.MethodGet, appleAccountTokenPath, nil, &token, now)
	if err != nil {
		return channel, err
	}
	if strings.TrimSpace(next.Scnt) == "" {
		if err := c.warmPortal(ctx, &next, now); err != nil {
			return channel, err
		}
		token.TimeOutInterval = 0
		next.Scnt = ""
		tokenStatus, err := c.request(ctx, &next, "", http.MethodGet, appleAccountTokenPath, nil, &token, now)
		if err != nil {
			return channel, err
		}
		if strings.TrimSpace(next.Scnt) == "" {
			return channel, appleAccountResponseError("session_invalid", "Apple Account session is invalid.", "token", tokenStatus)
		}
	}
	var manage struct {
		APIKey string `json:"apiKey"`
	}
	manageStatus, err := c.request(ctx, &next, "", http.MethodGet, "/account/manage", nil, &manage, now)
	if err != nil {
		return next, err
	}
	if apiKey := strings.TrimSpace(manage.APIKey); apiKey != "" {
		if !validICloudImportValue(apiKey, iCloudAppleAccountValueMaxLength) {
			return next, appleAccountResponseError("provider_response", "Apple Account returned an invalid API key.", "manage", manageStatus)
		}
		next.APIKey = apiKey
	}
	if strings.TrimSpace(next.APIKey) == "" {
		return next, appleAccountResponseError("provider_response", "Apple Account did not return an API key.", "manage", manageStatus)
	}
	if token.TimeOutInterval > 0 {
		expiresAt := now.Add(time.Duration(token.TimeOutInterval) * time.Minute)
		next.ManageExpiresAt = &expiresAt
	}
	return next, nil
}

func (c *AppleAccountClient) list(ctx context.Context, channel iCloudResourceChannelModel, now time.Time) (hmeListResult, iCloudResourceChannelModel, error) {
	if strings.TrimSpace(channel.APIKey) == "" {
		return hmeListResult{}, channel, &appleAccountError{Category: "invalid_context", SafeMessage: "Apple Account API key is missing."}
	}
	next := channel
	var payload struct {
		PrivateEmailList         *[]appleAccountPrivateEmail `json:"privateEmailList"`
		InactivePrivateEmailList []appleAccountPrivateEmail  `json:"inactivePrivateEmailList"`
		ForwardToEmailAddress    string                      `json:"forwardToEmailAddress"`
		MaxLimitReached          bool                        `json:"maxLimitReached"`
	}
	status, err := c.request(ctx, &next, next.APIKey, http.MethodGet, appleAccountPrivateEmailPath, nil, &payload, now)
	if err != nil {
		return hmeListResult{}, next, err
	}
	if payload.PrivateEmailList == nil {
		return hmeListResult{}, next, appleAccountResponseError("provider_response", "Apple Account returned an incomplete alias snapshot.", "list", status)
	}
	forwardTo := strings.ToLower(strings.TrimSpace(payload.ForwardToEmailAddress))
	if forwardTo != "" && !validICloudHMEEmail(forwardTo) {
		return hmeListResult{}, next, appleAccountResponseError("provider_response", "Apple Account returned an invalid forwarding target.", "list", status)
	}
	result := hmeListResult{
		SelectedForwardTo: forwardTo,
		Aliases:           make([]hmeAlias, 0, len(*payload.PrivateEmailList)+len(payload.InactivePrivateEmailList)),
		Complete:          true,
		MaxLimitReached:   payload.MaxLimitReached,
	}
	if forwardTo != "" {
		result.ForwardToEmails = []string{forwardTo}
	}
	seenIDs := make(map[string]struct{}, cap(result.Aliases))
	seenEmails := make(map[string]struct{}, cap(result.Aliases))
	appendAliases := func(items []appleAccountPrivateEmail, defaultActive bool) error {
		for _, item := range items {
			active := defaultActive
			if item.Active != nil {
				active = *item.Active
			}
			alias := hmeAlias{
				AnonymousID:    strings.TrimSpace(item.ID),
				Email:          strings.ToLower(strings.TrimSpace(item.EmailAddress)),
				Label:          strings.TrimSpace(item.Label),
				Note:           strings.TrimSpace(item.Note),
				ForwardToEmail: strings.ToLower(strings.TrimSpace(item.ForwardToEmail)),
				Origin:         "APPLE_ACCOUNT",
				Active:         active,
			}
			if alias.ForwardToEmail == "" {
				alias.ForwardToEmail = forwardTo
			}
			if !validICloudHMEText(alias.AnonymousID, iCloudHMEAnonymousIDMaxLength, false) ||
				!validICloudHMEEmail(alias.Email) ||
				!validICloudHMEText(alias.Label, iCloudHMELabelMaxLength, true) ||
				!validICloudHMEText(alias.Note, iCloudHMENoteMaxLength, true) ||
				!validICloudHMEEmail(alias.ForwardToEmail) {
				return appleAccountResponseError("provider_response", "Apple Account returned an invalid alias response.", "list", status)
			}
			if _, exists := seenIDs[alias.AnonymousID]; exists {
				return appleAccountResponseError("provider_response", "Apple Account returned duplicate aliases.", "list", status)
			}
			if _, exists := seenEmails[alias.Email]; exists {
				return appleAccountResponseError("provider_response", "Apple Account returned duplicate aliases.", "list", status)
			}
			seenIDs[alias.AnonymousID] = struct{}{}
			seenEmails[alias.Email] = struct{}{}
			result.Aliases = append(result.Aliases, alias)
		}
		return nil
	}
	if err := appendAliases(*payload.PrivateEmailList, true); err != nil {
		return hmeListResult{}, next, err
	}
	if err := appendAliases(payload.InactivePrivateEmailList, false); err != nil {
		return hmeListResult{}, next, err
	}
	return result, next, nil
}

func (c *AppleAccountClient) create(ctx context.Context, channel iCloudResourceChannelModel, now time.Time) (hmeAlias, iCloudResourceChannelModel, error) {
	if strings.TrimSpace(channel.APIKey) == "" {
		return hmeAlias{}, channel, &appleAccountError{Category: "invalid_context", SafeMessage: "Apple Account API key is missing."}
	}
	next := channel
	var generated struct {
		EmailAddress string `json:"emailAddress"`
	}
	generatedStatus, err := c.request(ctx, &next, next.APIKey, http.MethodPost, appleAccountPrivateEmailAddPath, map[string]any{}, &generated, now)
	if err != nil {
		return hmeAlias{}, next, err
	}
	generated.EmailAddress = strings.ToLower(strings.TrimSpace(generated.EmailAddress))
	if !validICloudHMEEmail(generated.EmailAddress) {
		return hmeAlias{}, next, appleAccountResponseError("provider_response", "Apple Account returned an invalid alias candidate.", "create", generatedStatus)
	}
	var completed struct {
		EmailAddress string `json:"emailAddress"`
		Label        string `json:"label"`
		Note         string `json:"note"`
		ID           string `json:"id"`
		Active       bool   `json:"active"`
	}
	completedStatus, err := c.request(ctx, &next, next.APIKey, http.MethodPut, appleAccountPrivateEmailAddCompletePath, map[string]string{
		"emailAddress": generated.EmailAddress,
		"label":        "ReMail",
		"note":         "",
	}, &completed, now)
	if err != nil {
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
		Active:      completed.Active,
	}
	if !validICloudHMEText(alias.AnonymousID, iCloudHMEAnonymousIDMaxLength, false) || !validICloudHMEEmail(alias.Email) {
		return hmeAlias{}, next, appleAccountResponseError("provider_response", "Apple Account returned an invalid created alias.", "create", completedStatus)
	}
	var detail struct {
		EmailAddress   string `json:"emailAddress"`
		Label          string `json:"label"`
		Note           string `json:"note"`
		ID             string `json:"id"`
		ForwardToEmail string `json:"forwardToEmail"`
		Active         *bool  `json:"active"`
	}
	detailPath := "/account/manage/email/private/" + url.PathEscape(alias.AnonymousID) + ".em"
	detailStatus, err := c.request(ctx, &next, next.APIKey, http.MethodGet, detailPath, nil, &detail, now)
	if err != nil {
		return hmeAlias{}, next, err
	}
	if value := strings.ToLower(strings.TrimSpace(detail.EmailAddress)); value != "" {
		alias.Email = value
	}
	if value := strings.TrimSpace(detail.Label); value != "" {
		alias.Label = value
	}
	if value := strings.TrimSpace(detail.Note); value != "" {
		alias.Note = value
	}
	alias.ForwardToEmail = strings.ToLower(strings.TrimSpace(detail.ForwardToEmail))
	if detail.Active != nil {
		alias.Active = *detail.Active
	}
	if !validICloudHMEEmail(alias.Email) || !validICloudHMEEmail(alias.ForwardToEmail) {
		return hmeAlias{}, next, appleAccountResponseError("provider_response", "Apple Account returned an invalid forwarding target.", "detail", detailStatus)
	}
	return alias, next, nil
}

func (c *AppleAccountClient) request(
	ctx context.Context,
	channel *iCloudResourceChannelModel,
	apiKey, method, requestPath string,
	body, result any,
	now time.Time,
) (int, error) {
	stage := appleAccountRequestStage(requestPath)
	if channel == nil {
		return 0, &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	scnt := strings.TrimSpace(channel.Scnt)
	if (channel.Host != "appleid.apple.com" && channel.Host != "appleid.apple.com.cn") ||
		!validAppleAccountCookie(channel.Cookie) ||
		(scnt != "" && !validICloudImportValue(scnt, iCloudAppleAccountValueMaxLength)) ||
		(requestPath != appleAccountTokenPath && scnt == "") {
		return 0, &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	endpoint := url.URL{Scheme: "https", Host: channel.Host, Path: requestPath}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return 0, &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	origin := strings.TrimRight(strings.TrimSpace(channel.Origin), "/")
	if origin == "" {
		origin = defaultAppleAccountOrigin(channel.Host)
	}
	userAgent, clientInfo := appleAccountRequestFingerprint(*channel, now)
	channel.UserAgent = userAgent
	channel.FDClientInfo = clientInfo
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("Referer", origin+"/")
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept-Language", appleweb.AcceptLanguage)
	request.Header.Set("Cookie", channel.Cookie)
	if scnt != "" {
		request.Header.Set("scnt", scnt)
	}
	request.Header.Set("X-Apple-I-Request-Context", "ca")
	request.Header.Set("X-Apple-I-TimeZone", appleweb.TimeZone)
	request.Header.Set("X-Apple-I-FD-Client-Info", clientInfo)
	request.Header.Set("Sec-Fetch-Site", "same-site")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	setAppleBrowserClientHints(request.Header, userAgent)
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		request.Header.Set("X-Apple-Api-Key", apiKey)
	}
	client := c
	if client == nil || client.httpClient == nil {
		client = NewAppleAccountClient(nil)
	}
	response, err := client.httpClient.Do(request)
	if err != nil || response == nil || response.Body == nil {
		return 0, &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account is temporarily unavailable.", Stage: stage}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, appleAccountResponseMaxBytes+1))
	if readErr != nil || len(data) > appleAccountResponseMaxBytes {
		return response.StatusCode, &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account returned an unreadable response.", Stage: stage, HTTPStatus: response.StatusCode}
	}
	responseScnt := strings.TrimSpace(response.Header.Get("scnt"))
	if responseScnt != "" && !validICloudImportValue(responseScnt, iCloudAppleAccountValueMaxLength) {
		return response.StatusCode, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid session context.", Stage: stage, HTTPStatus: response.StatusCode}
	}
	challengeBootstrap := requestPath == appleAccountTokenPath && scnt == "" &&
		responseScnt != "" &&
		(response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == 419)
	if challengeBootstrap {
		channel.Scnt = responseScnt
		return response.StatusCode, nil
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == 419 {
		return response.StatusCode, &appleAccountError{Category: "session_invalid", SafeMessage: "Apple Account session is invalid.", Stage: stage, HTTPStatus: response.StatusCode}
	}
	if response.StatusCode == http.StatusTooManyRequests || appleAccountBodyRateLimited(data) {
		return response.StatusCode, &appleAccountError{
			Category: "rate_limited", SafeMessage: "Apple Account alias creation is temporarily rate limited.",
			RetryAfter: iCloudResponseRetryAfter(response.Header.Get("Retry-After"), data, now), Stage: stage, HTTPStatus: response.StatusCode,
		}
	}
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500 {
		return response.StatusCode, &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account is temporarily unavailable.", Stage: stage, HTTPStatus: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, &appleAccountError{Category: "provider_rejected", SafeMessage: "Apple Account rejected the request.", Stage: stage, HTTPStatus: response.StatusCode}
	}
	updatedCookie := mergeICloudCookies(channel.Cookie, response.Cookies())
	if !validAppleAccountCookie(updatedCookie) {
		return response.StatusCode, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid session cookie.", Stage: stage, HTTPStatus: response.StatusCode}
	}
	channel.Cookie = updatedCookie
	if responseScnt != "" {
		channel.Scnt = responseScnt
	}
	if value := strings.TrimSpace(response.Header.Get("X-Apple-ID-Session-Id")); value != "" {
		if !validICloudImportValue(value, iCloudAppleAccountValueMaxLength) {
			return response.StatusCode, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid session identifier.", Stage: stage, HTTPStatus: response.StatusCode}
		}
		channel.SessionID = value
	}
	if value := strings.TrimSpace(response.Header.Get("X-Apple-I-DA-Token")); value != "" {
		if !validICloudImportValue(value, iCloudAppleAccountValueMaxLength) {
			return response.StatusCode, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid data access token.", Stage: stage, HTTPStatus: response.StatusCode}
		}
		channel.DataAccessToken = value
	}
	if result != nil && len(bytes.TrimSpace(data)) > 0 && json.Unmarshal(data, result) != nil {
		return response.StatusCode, &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid response.", Stage: stage, HTTPStatus: response.StatusCode}
	}
	return response.StatusCode, nil
}

func appleAccountResponseError(category, message, stage string, status int) *appleAccountError {
	return &appleAccountError{Category: category, SafeMessage: message, Stage: stage, HTTPStatus: status}
}

func appleAccountRequestStage(requestPath string) string {
	switch requestPath {
	case appleAccountTokenPath:
		return "token"
	case "/account/manage":
		return "manage"
	case appleAccountPrivateEmailPath:
		return "list"
	case appleAccountPrivateEmailAddPath, appleAccountPrivateEmailAddCompletePath:
		return "create"
	default:
		if strings.HasPrefix(requestPath, appleAccountPrivateEmailPath+"/") {
			return "detail"
		}
		return "request"
	}
}

func (c *AppleAccountClient) warmPortal(ctx context.Context, channel *iCloudResourceChannelModel, now time.Time) error {
	if err := c.portalRequest(ctx, channel, "/account/manage/section/privacy", false, now); err != nil {
		return err
	}
	return c.portalRequest(ctx, channel, "/bootstrap/portal", true, now)
}

func (c *AppleAccountClient) portalRequest(ctx context.Context, channel *iCloudResourceChannelModel, requestPath string, jsonContent bool, now time.Time) error {
	if channel == nil || (channel.Host != "appleid.apple.com" && channel.Host != "appleid.apple.com.cn") || !validAppleAccountCookie(channel.Cookie) {
		return &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	portalBase, err := url.Parse(defaultAppleAccountOrigin(channel.Host))
	if err != nil {
		return &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	endpoint := url.URL{Scheme: portalBase.Scheme, Host: portalBase.Host, Path: requestPath}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return &appleAccountError{Category: "invalid_context", SafeMessage: "Invalid Apple Account request context."}
	}
	userAgent, clientInfo := appleAccountRequestFingerprint(*channel, now)
	channel.UserAgent = userAgent
	channel.FDClientInfo = clientInfo
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Referer", portalBase.String()+"/")
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept-Language", appleweb.AcceptLanguage)
	request.Header.Set("Cookie", channel.Cookie)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Sec-Fetch-Mode", "navigate")
	request.Header.Set("Sec-Fetch-Dest", "document")
	setAppleBrowserClientHints(request.Header, userAgent)
	if jsonContent {
		request.Header.Set("Accept", "application/json, text/plain, */*")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", portalBase.String())
		request.Header.Set("Sec-Fetch-Mode", "cors")
		request.Header.Set("Sec-Fetch-Dest", "empty")
		request.Header.Set("X-Apple-I-Request-Context", "ca")
		request.Header.Set("X-Apple-I-TimeZone", appleweb.TimeZone)
		request.Header.Set("X-Apple-I-FD-Client-Info", clientInfo)
	}
	client := c
	if client == nil || client.httpClient == nil {
		client = NewAppleAccountClient(nil)
	}
	response, err := client.httpClient.Do(request)
	if err != nil || response == nil || response.Body == nil {
		return &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account is temporarily unavailable.", Stage: "portal"}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, appleAccountResponseMaxBytes+1))
	if readErr != nil || len(data) > appleAccountResponseMaxBytes {
		return &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account returned an unreadable response.", Stage: "portal", HTTPStatus: response.StatusCode}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == 419 {
		return &appleAccountError{Category: "session_invalid", SafeMessage: "Apple Account session is invalid.", Stage: "portal", HTTPStatus: response.StatusCode}
	}
	if response.StatusCode == http.StatusTooManyRequests || appleAccountBodyRateLimited(data) {
		return &appleAccountError{
			Category: "rate_limited", SafeMessage: "Apple Account alias creation is temporarily rate limited.",
			RetryAfter: iCloudResponseRetryAfter(response.Header.Get("Retry-After"), data, now), Stage: "portal", HTTPStatus: response.StatusCode,
		}
	}
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500 {
		return &appleAccountError{Category: "provider_unavailable", SafeMessage: "Apple Account is temporarily unavailable.", Stage: "portal", HTTPStatus: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &appleAccountError{Category: "provider_rejected", SafeMessage: "Apple Account rejected the request.", Stage: "portal", HTTPStatus: response.StatusCode}
	}
	updatedCookie := mergeICloudCookies(channel.Cookie, response.Cookies())
	if !validAppleAccountCookie(updatedCookie) {
		return &appleAccountError{Category: "provider_response", SafeMessage: "Apple Account returned an invalid session cookie.", Stage: "portal", HTTPStatus: response.StatusCode}
	}
	channel.Cookie = updatedCookie
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

func appleAccountRequestFingerprint(channel iCloudResourceChannelModel, now time.Time) (string, string) {
	userAgent := strings.TrimSpace(channel.UserAgent)
	if userAgent == "" {
		userAgent = appleweb.UserAgent
	}
	return userAgent, appleAccountRequestFDClientInfo(channel, userAgent, now)
}

func appleAccountRequestFDClientInfo(channel iCloudResourceChannelModel, userAgent string, now time.Time) string {
	if value := strings.TrimSpace(channel.FDClientInfo); value != "" && validICloudCurlHeader(value) {
		return value
	}
	if now.IsZero() {
		now = time.Now()
	}
	if value, err := appleweb.FDClientInfoFor(userAgent, now); err == nil {
		return value
	}
	return appleAccountFDClientInfo(userAgent)
}

func setAppleBrowserClientHints(header http.Header, userAgent string) {
	profile, ok := appleweb.BrowserProfileForUserAgent(userAgent)
	if !ok {
		return
	}
	header.Set("Sec-CH-UA", profile.SecCHUA)
	header.Set("Sec-CH-UA-Mobile", "?0")
	header.Set("Sec-CH-UA-Platform", profile.SecCHPlatform)
}
