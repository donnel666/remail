package icloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultICloudHMEUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"

const (
	iCloudHMEEmailMaxLength       = 320
	iCloudHMEAnonymousIDMaxLength = 191
	iCloudHMELabelMaxLength       = 500
	iCloudHMENoteMaxLength        = 2000
	iCloudHMEOriginMaxLength      = 64
	iCloudHMEResponseMaxBytes     = 10 << 20
)

var iCloudHMEHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?-maildomainws\.icloud\.com(?:\.cn)?$`)

var requiredICloudCookieNames = []string{
	"X-APPLE-DS-WEB-SESSION-TOKEN",
	"X-APPLE-WEBAUTH-USER",
	"X-APPLE-WEBAUTH-TOKEN",
}

type hmeConfig struct {
	Host                  string
	DSID                  string
	ClientID              string
	ClientBuildNumber     string
	ClientMasteringNumber string
	Cookie                string
	LangCode              string
	Origin                string
	Referer               string
	UserAgent             string
}

type hmeAlias struct {
	AnonymousID       string
	Email             string
	Label             string
	Note              string
	Origin            string
	Active            bool
	ProviderCreatedAt *time.Time
}

type hmeListResult struct {
	Aliases       []hmeAlias
	UpdatedCookie string
	Complete      bool
}

// hmeError is intentionally limited to a provider-safe category and message.
// It never carries an HTTP URL, response body, or Cookie value.
type hmeError struct {
	Category      string
	SafeMessage   string
	Retryable     bool
	RetryAfter    time.Duration
	SessionKnown  bool
	SessionValid  bool
	UpdatedCookie string
}

func (e *hmeError) Error() string {
	if e == nil || e.SafeMessage == "" {
		return "iCloud HME request failed."
	}
	return e.SafeMessage
}

// HMEClient owns the legacy provisioning surface: list, generate and reserve.
// It keeps Apple session material inside the request/response path.
type HMEClient struct {
	httpClient *http.Client
}

func NewHMEClient(client *http.Client) *HMEClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// HME is a single-host API. Do not let a provider redirect carry the
	// session Cookie to an unrelated host.
	if client.CheckRedirect == nil {
		clientCopy := *client
		clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		client = &clientCopy
	}
	return &HMEClient{httpClient: client}
}

func (c *HMEClient) list(ctx context.Context, config hmeConfig) (hmeListResult, error) {
	config = normalizeHMEConfig(config)
	currentCookie := config.Cookie
	result := hmeListResult{Aliases: make([]hmeAlias, 0)}
	var expectedTotal *int
	nextToken, nextTokenKey := "", ""
	seenTokens := make(map[string]struct{})
	seenAnonymousIDs := make(map[string]struct{})
	seenEmails := make(map[string]struct{})
	for page := 0; page < 1024; page++ {
		pageConfig := config
		pageConfig.Cookie = currentCookie
		extra := url.Values{}
		if nextToken != "" {
			extra.Set(nextTokenKey, nextToken)
		}
		body, updatedCookie, err := c.request(ctx, pageConfig, http.MethodGet, "/v2/hme/list", nil, extra)
		if err != nil {
			if providerErr, ok := err.(*hmeError); ok && providerErr.UpdatedCookie == "" && currentCookie != config.Cookie {
				providerErrCopy := *providerErr
				providerErrCopy.UpdatedCookie = currentCookie
				err = &providerErrCopy
			}
			return hmeListResult{}, err
		}
		currentCookie = updatedCookie
		payload, err := parseHMESuccessPayload(body, currentCookie)
		if err != nil {
			return hmeListResult{}, err
		}
		if payload.Result.HMEEmails == nil {
			return hmeListResult{}, hmeResponseError("snapshot_incomplete", "iCloud HME alias snapshot is incomplete.", true, currentCookie)
		}
		for _, item := range *payload.Result.HMEEmails {
			alias, aliasErr := parseHMEAlias(item)
			if aliasErr != nil {
				return hmeListResult{}, hmeResponseError("provider_response", "iCloud HME returned an invalid alias response.", true, currentCookie)
			}
			if _, exists := seenAnonymousIDs[alias.AnonymousID]; exists {
				return hmeListResult{}, hmeResponseError("provider_response", "iCloud HME returned duplicate aliases.", true, currentCookie)
			}
			if _, exists := seenEmails[alias.Email]; exists {
				return hmeListResult{}, hmeResponseError("provider_response", "iCloud HME returned duplicate aliases.", true, currentCookie)
			}
			seenAnonymousIDs[alias.AnonymousID] = struct{}{}
			seenEmails[alias.Email] = struct{}{}
			result.Aliases = append(result.Aliases, alias)
		}
		if payload.Result.Total != nil {
			if *payload.Result.Total < 0 || (expectedTotal != nil && *expectedTotal != *payload.Result.Total) {
				return hmeListResult{}, hmeResponseError("provider_response", "iCloud HME returned an invalid alias total.", true, currentCookie)
			}
			total := *payload.Result.Total
			expectedTotal = &total
		}
		nextToken, nextTokenKey = hmeNextToken(payload.Result)
		if payload.Result.HasMore != nil && !*payload.Result.HasMore && nextToken != "" {
			return hmeListResult{}, hmeResponseError("snapshot_incomplete", "iCloud HME alias snapshot is incomplete.", true, currentCookie)
		}
		hasMore := nextToken != ""
		if payload.Result.HasMore != nil {
			hasMore = *payload.Result.HasMore
		}
		if !hasMore {
			if expectedTotal != nil && *expectedTotal != len(result.Aliases) {
				return hmeListResult{}, hmeResponseError("snapshot_incomplete", "iCloud HME alias snapshot is incomplete.", true, currentCookie)
			}
			result.UpdatedCookie = currentCookie
			result.Complete = true
			return result, nil
		}
		if nextToken == "" || nextTokenKey == "" {
			return hmeListResult{}, hmeResponseError("snapshot_incomplete", "iCloud HME alias snapshot is incomplete.", true, currentCookie)
		}
		if _, exists := seenTokens[nextTokenKey+"\x00"+nextToken]; exists {
			return hmeListResult{}, hmeResponseError("snapshot_incomplete", "iCloud HME alias snapshot is incomplete.", true, currentCookie)
		}
		seenTokens[nextTokenKey+"\x00"+nextToken] = struct{}{}
	}
	return hmeListResult{}, hmeResponseError("snapshot_incomplete", "iCloud HME alias snapshot is incomplete.", true, currentCookie)
}

func (c *HMEClient) Generate(ctx context.Context, config hmeConfig) (string, string, error) {
	body, updatedCookie, err := c.request(ctx, config, http.MethodPost, "/v1/hme/generate", map[string]string{"langCode": normalizeHMEConfig(config).LangCode}, nil)
	if err != nil {
		return "", "", err
	}
	payload, err := parseHMESuccessPayload(body, updatedCookie)
	if err != nil {
		return "", "", err
	}
	var candidate string
	if payload.Result == nil || json.Unmarshal(payload.Result.HME, &candidate) != nil || !validICloudHMEEmail(strings.ToLower(strings.TrimSpace(candidate))) {
		return "", "", hmeResponseError("provider_response", "iCloud HME returned an invalid generated alias.", true, updatedCookie)
	}
	return strings.ToLower(strings.TrimSpace(candidate)), updatedCookie, nil
}

func (c *HMEClient) reserve(ctx context.Context, config hmeConfig, candidate, label, note string) (hmeAlias, string, error) {
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	label = strings.TrimSpace(label)
	note = strings.TrimSpace(note)
	if !validICloudHMEEmail(candidate) || !validICloudHMEText(label, iCloudHMELabelMaxLength, true) || !validICloudHMEText(note, iCloudHMENoteMaxLength, true) {
		return hmeAlias{}, "", &hmeError{Category: "invalid_context", SafeMessage: "Invalid iCloud HME request context."}
	}
	body, updatedCookie, err := c.request(ctx, config, http.MethodPost, "/v1/hme/reserve", map[string]string{"hme": candidate, "label": label, "note": note}, nil)
	if err != nil {
		return hmeAlias{}, "", err
	}
	payload, err := parseHMESuccessPayload(body, updatedCookie)
	if err != nil || payload.Result == nil {
		if err != nil {
			return hmeAlias{}, "", err
		}
		return hmeAlias{}, "", hmeResponseError("provider_response", "iCloud HME returned an invalid reserved alias.", true, updatedCookie)
	}
	var item hmePayloadAlias
	if json.Unmarshal(payload.Result.HME, &item) != nil {
		return hmeAlias{}, "", hmeResponseError("provider_response", "iCloud HME returned an invalid reserved alias.", true, updatedCookie)
	}
	alias, err := parseHMEAlias(item)
	if err != nil {
		return hmeAlias{}, "", hmeResponseError("provider_response", "iCloud HME returned an invalid reserved alias.", true, updatedCookie)
	}
	return alias, updatedCookie, nil
}

func parseHMEAcknowledgement(body []byte, updatedCookie string) (*hmeSuccessPayload, error) {
	var payload hmeSuccessPayload
	if json.Unmarshal(body, &payload) != nil || payload.Success == nil {
		return nil, hmeResponseError("provider_response", "iCloud HME returned an invalid response.", true, updatedCookie)
	}
	if !*payload.Success {
		if providerErr := hmeProviderErrorResponse(body, updatedCookie); providerErr != nil {
			return nil, providerErr
		}
		return nil, hmeResponseError("provider_rejected", "iCloud HME request was rejected.", false, updatedCookie)
	}
	return &payload, nil
}

type hmePayloadAlias struct {
	HME             string `json:"hme"`
	Label           string `json:"label"`
	Note            string `json:"note"`
	Origin          string `json:"origin"`
	IsActive        bool   `json:"isActive"`
	CreateTimestamp int64  `json:"createTimestamp"`
	AnonymousID     string `json:"anonymousId"`
}

type hmePayloadResult struct {
	HMEEmails         *[]hmePayloadAlias `json:"hmeEmails"`
	HME               json.RawMessage    `json:"hme"`
	Total             *int               `json:"total"`
	HasMore           *bool              `json:"hasMore"`
	NextToken         string             `json:"nextToken"`
	ContinuationToken string             `json:"continuationToken"`
	NextPageToken     string             `json:"nextPageToken"`
}

type hmeSuccessPayload struct {
	Success *bool             `json:"success"`
	Result  *hmePayloadResult `json:"result"`
}

type hmeProviderErrorPayload struct {
	ErrorCode json.RawMessage `json:"errorCode"`
	Code      json.RawMessage `json:"code"`
	Error     struct {
		ErrorCode json.RawMessage `json:"errorCode"`
		Code      json.RawMessage `json:"code"`
	} `json:"error"`
}

func parseHMESuccessPayload(body []byte, updatedCookie string) (*hmeSuccessPayload, error) {
	payload, err := parseHMEAcknowledgement(body, updatedCookie)
	if err != nil {
		return nil, err
	}
	if payload.Result == nil {
		return nil, hmeResponseError("provider_response", "iCloud HME returned an invalid response.", true, updatedCookie)
	}
	return payload, nil
}

func hmeProviderErrorResponse(body []byte, updatedCookie string) *hmeError {
	var payload hmeProviderErrorPayload
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	code := hmeProviderErrorCode(payload.ErrorCode, payload.Code, payload.Error.ErrorCode, payload.Error.Code)
	switch code {
	case "-41003":
		return hmeResponseError("invalid_candidate", "iCloud rejected the generated alias candidate.", true, updatedCookie)
	case "-41015":
		return hmeResponseError("rate_limited", "iCloud alias creation is temporarily rate limited.", true, updatedCookie)
	default:
		return nil
	}
}

func hmeProviderErrorCode(values ...json.RawMessage) string {
	for _, raw := range values {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			text = strings.TrimSpace(text)
			if validICloudHMEText(text, 80, false) {
				return text
			}
			continue
		}
		var number json.Number
		if json.Unmarshal(raw, &number) == nil {
			return number.String()
		}
	}
	return ""
}

func parseHMEAlias(item hmePayloadAlias) (hmeAlias, error) {
	anonymousID := strings.TrimSpace(item.AnonymousID)
	email := strings.ToLower(strings.TrimSpace(item.HME))
	label := strings.TrimSpace(item.Label)
	note := strings.TrimSpace(item.Note)
	origin := strings.TrimSpace(item.Origin)
	if !validICloudHMEText(anonymousID, iCloudHMEAnonymousIDMaxLength, false) ||
		!validICloudHMEEmail(email) ||
		!validICloudHMEText(label, iCloudHMELabelMaxLength, true) ||
		!validICloudHMEText(note, iCloudHMENoteMaxLength, true) ||
		!validICloudHMEText(origin, iCloudHMEOriginMaxLength, true) {
		return hmeAlias{}, &hmeError{Category: "provider_response", SafeMessage: "iCloud HME returned an invalid alias response."}
	}
	var createdAt *time.Time
	if item.CreateTimestamp > 0 {
		value := time.UnixMilli(item.CreateTimestamp).UTC()
		createdAt = &value
	}
	return hmeAlias{
		AnonymousID: anonymousID, Email: email, Label: label, Note: note,
		Origin: origin, Active: item.IsActive, ProviderCreatedAt: createdAt,
	}, nil
}

func hmeNextToken(result *hmePayloadResult) (string, string) {
	if result == nil {
		return "", ""
	}
	if value := strings.TrimSpace(result.NextToken); value != "" {
		return value, "nextToken"
	}
	if value := strings.TrimSpace(result.ContinuationToken); value != "" {
		return value, "continuationToken"
	}
	if value := strings.TrimSpace(result.NextPageToken); value != "" {
		return value, "nextPageToken"
	}
	return "", ""
}

func (c *HMEClient) request(ctx context.Context, config hmeConfig, method, requestPath string, payload any, extra url.Values) ([]byte, string, error) {
	config = normalizeHMEConfig(config)
	if !validICloudHMEHost(config.Host) || config.DSID == "" || config.ClientID == "" || config.ClientBuildNumber == "" || config.ClientMasteringNumber == "" {
		return nil, "", &hmeError{Category: "invalid_context", SafeMessage: "Invalid iCloud HME request context."}
	}
	if !validICloudImportCookie(config.Cookie) {
		return nil, "", &hmeError{Category: "session_invalid", SafeMessage: "iCloud session is invalid.", SessionKnown: true, SessionValid: false}
	}
	endpoint := url.URL{Scheme: "https", Host: config.Host, Path: requestPath}
	query := url.Values{}
	query.Set("clientBuildNumber", config.ClientBuildNumber)
	query.Set("clientMasteringNumber", config.ClientMasteringNumber)
	query.Set("clientId", config.ClientID)
	query.Set("dsid", config.DSID)
	for key, values := range extra {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	endpoint.RawQuery = query.Encode()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, "", &hmeError{Category: "invalid_context", SafeMessage: "Invalid iCloud HME request context."}
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, "", &hmeError{Category: "invalid_context", SafeMessage: "Invalid iCloud HME request context."}
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Language", iCloudAcceptLanguage(config.LangCode))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Origin", config.Origin)
	request.Header.Set("Referer", config.Referer)
	request.Header.Set("User-Agent", config.UserAgent)
	request.Header.Set("Cookie", config.Cookie)
	client := c
	if client == nil || client.httpClient == nil {
		client = NewHMEClient(nil)
	}
	response, err := client.httpClient.Do(request)
	if err != nil || response == nil || response.Body == nil {
		return nil, "", &hmeError{Category: "provider_unavailable", SafeMessage: "iCloud HME service is temporarily unavailable.", Retryable: true}
	}
	defer response.Body.Close()
	updatedCookie := mergeICloudCookies(config.Cookie, response.Cookies())
	if len(updatedCookie) > iCloudImportCookieMaxBytes {
		return nil, "", &hmeError{Category: "provider_response", SafeMessage: "iCloud HME returned an oversized session cookie.", SessionKnown: true, SessionValid: response.StatusCode < 400}
	}
	if !validICloudImportCookie(updatedCookie) {
		return nil, "", &hmeError{Category: "session_invalid", SafeMessage: "iCloud session is invalid.", SessionKnown: true, SessionValid: false}
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, iCloudHMEResponseMaxBytes+1))
	if err != nil {
		return nil, "", hmeResponseError("provider_unavailable", "iCloud HME service is temporarily unavailable.", true, updatedCookie)
	}
	if len(responseBody) > iCloudHMEResponseMaxBytes {
		return nil, "", hmeResponseError("provider_response", "iCloud HME returned an oversized response.", true, updatedCookie)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == 421 {
		return nil, "", &hmeError{Category: "session_invalid", SafeMessage: "iCloud session is invalid.", SessionKnown: true, SessionValid: false, UpdatedCookie: updatedCookie}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		providerErr := hmeResponseError("rate_limited", "iCloud alias creation is temporarily rate limited.", true, updatedCookie)
		providerErr.RetryAfter = iCloudRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return nil, "", providerErr
	}
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500 {
		return nil, "", hmeResponseError("provider_unavailable", "iCloud HME service is temporarily unavailable.", true, updatedCookie)
	}
	if response.StatusCode >= 400 {
		if providerErr := hmeProviderErrorResponse(responseBody, updatedCookie); providerErr != nil {
			providerErr.RetryAfter = iCloudRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
			return nil, "", providerErr
		}
		return nil, "", &hmeError{Category: "provider_rejected", SafeMessage: "iCloud HME request was rejected.", UpdatedCookie: updatedCookie}
	}
	return responseBody, updatedCookie, nil
}

func iCloudRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return min(time.Duration(seconds)*time.Second, 2*time.Hour)
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return min(retryAt.Sub(now), 2*time.Hour)
	}
	return 0
}

func hmeResponseError(category, message string, retryable bool, updatedCookie string) *hmeError {
	return &hmeError{Category: category, SafeMessage: message, Retryable: retryable, SessionKnown: true, SessionValid: true, UpdatedCookie: updatedCookie}
}

func validICloudHMEText(value string, maxLength int, allowEmpty bool) bool {
	if !allowEmpty && value == "" {
		return false
	}
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxLength && !strings.ContainsAny(value, "\r\n")
}

func validICloudHMEEmail(value string) bool {
	return validICloudHMEText(value, iCloudHMEEmailMaxLength, false) && isICloudImportEmail(value)
}

func normalizeHMEConfig(config hmeConfig) hmeConfig {
	config.Host = strings.ToLower(strings.TrimSpace(config.Host))
	config.DSID = strings.TrimSpace(config.DSID)
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.ClientBuildNumber = strings.TrimSpace(config.ClientBuildNumber)
	config.ClientMasteringNumber = strings.TrimSpace(config.ClientMasteringNumber)
	config.Cookie = strings.TrimSpace(config.Cookie)
	config.LangCode = strings.TrimSpace(config.LangCode)
	config.Origin = strings.TrimSpace(config.Origin)
	config.Referer = strings.TrimSpace(config.Referer)
	config.UserAgent = strings.TrimSpace(config.UserAgent)
	langCode, origin, referer := defaultICloudHMEContext(config.Host)
	if config.LangCode == "" {
		config.LangCode = langCode
	}
	if config.Origin == "" {
		config.Origin = origin
	}
	if config.Referer == "" {
		config.Referer = referer
	}
	if config.UserAgent == "" {
		config.UserAgent = defaultICloudHMEUserAgent
	}
	return config
}

func validICloudHMEHost(host string) bool {
	return iCloudHMEHostPattern.MatchString(strings.ToLower(strings.TrimSpace(host)))
}

func defaultICloudHMEContext(host string) (langCode, origin, referer string) {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(host)), ".icloud.com.cn") {
		return "zh-cn", "https://www.icloud.com.cn", "https://www.icloud.com.cn/"
	}
	return "zh-tw", "https://www.icloud.com", "https://www.icloud.com/"
}

func iCloudAcceptLanguage(langCode string) string {
	if strings.EqualFold(strings.TrimSpace(langCode), "zh-cn") {
		return "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7"
	}
	return "zh-TW,zh;q=0.9,en-US;q=0.8,en;q=0.7"
}

func hasRequiredICloudCookies(value string) bool {
	values := iCloudCookieValues(value)
	for _, name := range requiredICloudCookieNames {
		if strings.TrimSpace(values[name]) == "" {
			return false
		}
	}
	return true
}

func iCloudCookieValues(value string) map[string]string {
	values := make(map[string]string)
	for _, part := range strings.Split(value, ";") {
		name, rawValue, found := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			continue
		}
		values[name] = strings.TrimSpace(rawValue)
	}
	return values
}

func mergeICloudCookies(current string, updates []*http.Cookie) string {
	order := make([]string, 0)
	values := make(map[string]string)
	seenNames := make(map[string]struct{})
	for _, part := range strings.Split(current, ";") {
		name, value, found := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			continue
		}
		if _, exists := seenNames[name]; !exists {
			order = append(order, name)
			seenNames[name] = struct{}{}
		}
		values[name] = strings.TrimSpace(value)
	}
	for _, cookie := range updates {
		if cookie == nil || strings.TrimSpace(cookie.Name) == "" {
			continue
		}
		name := strings.TrimSpace(cookie.Name)
		if _, exists := seenNames[name]; !exists {
			order = append(order, name)
			seenNames[name] = struct{}{}
		}
		if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && cookie.Expires.Before(time.Now())) {
			delete(values, name)
			continue
		}
		values[name] = cookie.Value
	}
	pairs := make([]string, 0, len(order))
	for _, name := range order {
		if value, exists := values[name]; exists {
			pairs = append(pairs, name+"="+value)
		}
	}
	return strings.Join(pairs, "; ")
}
