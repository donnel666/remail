package icloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/appleweb"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/donnel666/remail/internal/platform"
)

const (
	appleOnboardingStateVersion = 1
	appleOnboardingAuthVersion  = "latest"
	appleOnboardingManageAuth   = "8.0.2"
	appleOnboardingSKVersion    = "7"
	appleOnboardingLanguage     = "zh-CN,zh;q=0.9"
)

var (
	appleOnboardingBootArgsPattern = regexp.MustCompile(`(?s)<script type="application/json"(?: class="boot_args"| id="boot_args")>\s*(.*?)</script>`)
	appleOnboardingHTMLAttrPattern = regexp.MustCompile(`(?i)%s="([^"]*)"`)
)

type appleOnboardingEndpoints struct {
	Account string
	IDMSA   string
	AppleID string
	ICloud  string
	Setup   string
}

func defaultAppleOnboardingEndpoints() appleOnboardingEndpoints {
	return appleOnboardingEndpoints{
		Account: "https://account.apple.com",
		IDMSA:   "https://idmsa.apple.com",
		AppleID: "https://appleid.apple.com",
		ICloud:  "https://www.icloud.com",
		Setup:   "https://setup.icloud.com",
	}
}

type appleOnboardingHTTPResponse struct {
	StatusCode int
	Body       string
	URL        string
	Header     http.Header
}

type appleOnboardingHTTPSession interface {
	Request(string, string, map[string]string, any, bool) (*appleOnboardingHTTPResponse, error)
	SnapshotCookies(...string) ([]msacl.SessionCookie, error)
	RestoreCookies([]msacl.SessionCookie) error
}

type appleOnboardingSessionFactory func(context.Context) (appleOnboardingHTTPSession, error)

type appleOnboardingMSACLSession struct{ session *msacl.Session }

func newAppleOnboardingSession(ctx context.Context) (appleOnboardingHTTPSession, error) {
	session, err := msacl.NewAppleAPISession(ctx, "", 30)
	if err != nil {
		return nil, err
	}
	return &appleOnboardingMSACLSession{session: session}, nil
}

func (s *appleOnboardingMSACLSession) Request(method, rawURL string, headers map[string]string, body any, follow bool) (*appleOnboardingHTTPResponse, error) {
	response, err := s.session.Request(method, rawURL, headers, body, follow)
	if err != nil {
		return nil, err
	}
	headersCopy := make(http.Header, len(response.Header))
	for key, values := range response.Header {
		key = http.CanonicalHeaderKey(key)
		headersCopy[key] = append(headersCopy[key], values...)
	}
	return &appleOnboardingHTTPResponse{StatusCode: response.StatusCode, Body: response.Body, URL: response.URL, Header: headersCopy}, nil
}

func (s *appleOnboardingMSACLSession) SnapshotCookies(rawURLs ...string) ([]msacl.SessionCookie, error) {
	return s.session.SnapshotCookies(rawURLs...)
}

func (s *appleOnboardingMSACLSession) RestoreCookies(cookies []msacl.SessionCookie) error {
	return s.session.RestoreCookies(cookies)
}

type appleOnboardingBrowserState struct {
	Version                int                     `json:"version"`
	Mode                   string                  `json:"mode,omitempty"`
	UserAgent              string                  `json:"userAgent,omitempty"`
	Cookies                []msacl.SessionCookie   `json:"cookies,omitempty"`
	Status                 int                     `json:"status,omitempty"`
	Location               string                  `json:"location,omitempty"`
	WidgetKey              string                  `json:"widgetKey,omitempty"`
	RedirectURI            string                  `json:"redirectUri,omitempty"`
	DomainID               string                  `json:"domainId,omitempty"`
	RememberMe             bool                    `json:"rememberMe,omitempty"`
	RequireGrant           bool                    `json:"requireGrant,omitempty"`
	OfferUpgrade           bool                    `json:"offerUpgrade,omitempty"`
	ServiceURL             string                  `json:"serviceUrl,omitempty"`
	AuthVersion            string                  `json:"authVersion,omitempty"`
	APIKey                 string                  `json:"apiKey,omitempty"`
	FrameID                string                  `json:"frameId,omitempty"`
	SetupClientID          string                  `json:"setupClientId,omitempty"`
	BuildNumber            string                  `json:"buildNumber,omitempty"`
	MasteringNumber        string                  `json:"masteringNumber,omitempty"`
	Locale                 string                  `json:"locale,omitempty"`
	PageLocale             string                  `json:"pageLocale,omitempty"`
	AccountCountry         string                  `json:"accountCountry,omitempty"`
	AccountLoginURL        string                  `json:"accountLoginUrl,omitempty"`
	GetTermsURL            string                  `json:"getTermsUrl,omitempty"`
	RepairDoneURL          string                  `json:"repairDoneUrl,omitempty"`
	Scnt                   map[string]string       `json:"scnt,omitempty"`
	SessionID              string                  `json:"sessionId,omitempty"`
	AuthAttributes         string                  `json:"authAttributes,omitempty"`
	HashcashBits           int                     `json:"hashcashBits,omitempty"`
	HashcashChallenge      string                  `json:"hashcashChallenge,omitempty"`
	OAuthContext           string                  `json:"oauthContext,omitempty"`
	RepairToken            string                  `json:"repairToken,omitempty"`
	GSToken                string                  `json:"gsToken,omitempty"`
	InviteToken            string                  `json:"inviteToken,omitempty"`
	FamilyOrganizerEmail   string                  `json:"familyOrganizerEmail,omitempty"`
	DSID                   string                  `json:"dsid,omitempty"`
	PremiumMailURL         string                  `json:"premiumMailUrl,omitempty"`
	HasQualifyingDevice    *bool                   `json:"hasQualifyingDevice,omitempty"`
	PendingTrustedPhoneID  json.RawMessage         `json:"pendingTrustedPhoneId,omitempty"`
	PendingPhoneLastTwo    string                  `json:"pendingPhoneLastTwo,omitempty"`
	PendingEnrollmentPhone json.RawMessage         `json:"pendingEnrollmentPhone,omitempty"`
	ForwardVerificationID  string                  `json:"forwardVerificationId,omitempty"`
	OldChannel             *AppleOnboardingChannel `json:"oldChannel,omitempty"`
}

type appleOnboardingFlow struct {
	ctx            context.Context
	http           appleOnboardingHTTPSession
	factory        appleOnboardingSessionFactory
	endpoints      appleOnboardingEndpoints
	now            func() time.Time
	state          appleOnboardingBrowserState
	lastHTTPStatus int
}

func loadAppleOnboardingFlow(ctx context.Context, client *appleOnboardingClient, raw json.RawMessage, email string) (*appleOnboardingFlow, error) {
	httpSession, err := client.newSession(ctx)
	if err != nil {
		return nil, err
	}
	flow := &appleOnboardingFlow{ctx: ctx, http: httpSession, factory: client.newSession, endpoints: client.endpoints, now: client.now}
	if flow.now == nil {
		flow.now = time.Now
	}
	flow.state = flow.blankState()
	if len(raw) == 0 {
		flow.state.UserAgent = appleweb.AutomatedBrowserProfile(email).UserAgent
		return flow, nil
	}
	if err := json.Unmarshal(raw, &flow.state); err != nil || flow.state.Version != appleOnboardingStateVersion {
		return nil, &AppleOnboardingError{Category: "invalid_session", SafeMessage: "Stored Apple onboarding session is invalid."}
	}
	if flow.state.Scnt == nil {
		flow.state.Scnt = make(map[string]string)
	}
	// Sessions created before browser identity was persisted keep their old
	// macOS fingerprint; only newly created automated sessions exclude macOS.
	if strings.TrimSpace(flow.state.UserAgent) == "" {
		flow.state.UserAgent = appleweb.UserAgent
	}
	if _, ok := appleweb.BrowserProfileForUserAgent(flow.state.UserAgent); !ok {
		return nil, &AppleOnboardingError{Category: "invalid_session", SafeMessage: "Stored Apple onboarding browser identity is invalid."}
	}
	if err := flow.http.RestoreCookies(flow.state.Cookies); err != nil {
		return nil, &AppleOnboardingError{Category: "invalid_session", SafeMessage: "Stored Apple onboarding cookies are invalid."}
	}
	return flow, nil
}

func (f *appleOnboardingFlow) blankState() appleOnboardingBrowserState {
	return appleOnboardingBrowserState{
		Version:     appleOnboardingStateVersion,
		ServiceURL:  strings.TrimRight(f.endpoints.IDMSA, "/") + "/appleauth",
		AuthVersion: appleOnboardingAuthVersion,
		Locale:      "zh_CN", PageLocale: "zh-cn", Scnt: make(map[string]string),
		AccountLoginURL: strings.TrimRight(f.endpoints.Setup, "/") + "/setup/ws/1/accountLogin",
		GetTermsURL:     strings.TrimRight(f.endpoints.Setup, "/") + "/setup/ws/1/getTerms",
		RepairDoneURL:   strings.TrimRight(f.endpoints.Setup, "/") + "/setup/ws/1/repairDone",
	}
}

func (f *appleOnboardingFlow) reset(mode string) error {
	oldChannel := f.state.OldChannel
	userAgent := f.state.UserAgent
	httpSession, err := f.factory(f.ctx)
	if err != nil {
		return err
	}
	f.http = httpSession
	f.state = f.blankState()
	f.state.Mode = mode
	f.state.UserAgent = userAgent
	f.state.OldChannel = oldChannel
	// The web flow uses opaque UUID v4 values for both identifiers.
	f.state.FrameID = platform.NewUUIDV4String()
	f.state.SetupClientID = platform.NewUUIDV4String()
	return nil
}

func (f *appleOnboardingFlow) browserProfile() appleweb.BrowserProfile {
	profile, _ := appleweb.BrowserProfileForUserAgent(f.state.UserAgent)
	return profile
}

func (f *appleOnboardingFlow) snapshot() (json.RawMessage, error) {
	urls := []string{f.endpoints.Account, f.endpoints.IDMSA, f.endpoints.AppleID, f.endpoints.ICloud, f.endpoints.Setup, f.state.ServiceURL, f.state.PremiumMailURL}
	filtered := urls[:0]
	for _, rawURL := range urls {
		if parsed, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && parsed.Scheme != "" && parsed.Hostname() != "" {
			filtered = append(filtered, parsed.Scheme+"://"+parsed.Host)
		}
	}
	cookies, err := f.http.SnapshotCookies(filtered...)
	if err != nil {
		return nil, err
	}
	f.state.Cookies = cookies
	return json.Marshal(f.state)
}

func (f *appleOnboardingFlow) request(method, rawURL string, body any, html, profile, sendHashcash, appID, noSession bool, accept string) ([]byte, error) {
	headers, err := f.headers(rawURL, html, profile, sendHashcash, appID, noSession, accept)
	if err != nil {
		return nil, err
	}
	response, err := f.http.Request(method, rawURL, headers, body, false)
	if err != nil {
		return nil, err
	}
	f.state.Status = response.StatusCode
	f.lastHTTPStatus = response.StatusCode
	f.absorb(response.Header, appleOnboardingHost(rawURL))
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= http.StatusInternalServerError {
		retryAt := appleOnboardingRetryAt(response.Header.Get("Retry-After"), f.now())
		return nil, &AppleOnboardingError{Category: "apple_unavailable", SafeMessage: "Apple service is temporarily unavailable.", Retryable: true, RetryAt: &retryAt}
	}
	return []byte(response.Body), nil
}

func (f *appleOnboardingFlow) getObject(rawURL, label string, html, profile, noSession bool, accept string) (map[string]any, error) {
	body, err := f.request(http.MethodGet, rawURL, nil, html, profile, false, false, noSession, accept)
	if err != nil {
		return nil, err
	}
	return decodeAppleOnboardingObject(body, label)
}

func (f *appleOnboardingFlow) postObject(rawURL string, payload any, label string, sendHashcash, profile, noSession bool) (map[string]any, error) {
	body, err := f.request(http.MethodPost, rawURL, payload, false, profile, sendHashcash, false, noSession, "application/json, text/javascript, */*; q=0.01")
	if err != nil {
		return nil, err
	}
	return decodeAppleOnboardingObject(body, label)
}

func (f *appleOnboardingFlow) putObject(rawURL string, payload any, label string, profile, appID, noSession bool) (map[string]any, error) {
	body, err := f.request(http.MethodPut, rawURL, payload, false, profile, false, appID, noSession, "application/json, text/javascript, */*; q=0.01")
	if err != nil {
		return nil, err
	}
	return decodeAppleOnboardingObject(body, label)
}

func (f *appleOnboardingFlow) headers(rawURL string, html, profile, sendHashcash, appID, noSession bool, accept string) (map[string]string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := parsed.Hostname()
	browser := f.browserProfile()
	clientInfo, err := appleweb.FDClientInfoFor(browser.UserAgent, f.now())
	if err != nil {
		return nil, err
	}
	if profile {
		headers := map[string]string{
			"User-Agent": browser.UserAgent, "Accept": "application/json, text/plain, */*", "Accept-Language": appleOnboardingLanguage,
			"Content-Type": "application/json", "Origin": f.endpoints.Account, "Referer": strings.TrimRight(f.endpoints.Account, "/") + "/",
			"X-Apple-I-FD-Client-Info": clientInfo, "X-Apple-I-Request-Context": "ca", "X-Apple-I-TimeZone": appleweb.TimeZone,
		}
		addAppleOnboardingLegacyClientHints(headers, browser)
		if f.state.APIKey != "" {
			headers["X-Apple-Api-Key"] = f.state.APIKey
		}
		if f.state.Scnt[host] != "" {
			headers["scnt"] = f.state.Scnt[host]
		}
		return headers, nil
	}
	origin := "https://" + parsed.Host
	headers := map[string]string{
		"User-Agent": browser.UserAgent, "Accept": accept, "Accept-Language": appleOnboardingLanguage, "Origin": origin,
		"Referer": strings.TrimRight(origin, "/") + "/", "X-Apple-I-FD-Client-Info": clientInfo,
		"X-Apple-I-TimeZone": appleweb.TimeZone, "X-Apple-Privacy-Consent": "true",
	}
	addAppleOnboardingLegacyClientHints(headers, browser)
	if !html {
		headers["Content-Type"] = "application/json"
		headers["X-Requested-With"] = "XMLHttpRequest"
	}
	if appleOnboardingURLWithin(rawURL, f.endpoints.ICloud) || appleOnboardingURLWithin(rawURL, f.endpoints.Setup) {
		headers["Origin"] = f.endpoints.ICloud
		headers["Referer"] = strings.TrimRight(f.endpoints.ICloud, "/") + "/"
	}
	if appleOnboardingURLWithin(rawURL, f.endpoints.Account) && f.state.InviteToken != "" {
		headers["Referer"] = strings.TrimRight(f.endpoints.Account, "/") + "/family/invite?token=" + url.QueryEscape(f.state.InviteToken)
		if f.state.GSToken != "" {
			headers["X-Apple-GS-Token"] = f.state.GSToken
		}
	}
	if f.state.SessionID != "" && !noSession {
		headers["X-Apple-ID-Session-Id"] = f.state.SessionID
	}
	if f.state.Scnt[host] != "" {
		headers["scnt"] = f.state.Scnt[host]
	}
	if appleOnboardingURLWithin(rawURL, f.endpoints.IDMSA) || appleOnboardingURLWithin(rawURL, f.state.ServiceURL) {
		headers["X-Apple-Domain-Id"] = f.state.DomainID
		headers["X-Apple-Frame-Id"] = f.state.FrameID
		headers["X-Apple-Locale"] = f.state.Locale
		headers["X-Apple-OAuth-Client-Id"] = f.state.WidgetKey
		headers["X-Apple-OAuth-Client-Type"] = "firstPartyAuth"
		headers["X-Apple-OAuth-Redirect-URI"] = f.state.RedirectURI
		headers["X-Apple-OAuth-Response-Mode"] = "web_message"
		headers["X-Apple-OAuth-Response-Type"] = "code"
		headers["X-Apple-OAuth-State"] = f.state.FrameID
		headers["X-Apple-Privacy-Consent-Accepted"] = "true"
		headers["X-Apple-Widget-Key"] = f.state.WidgetKey
		if f.state.RequireGrant {
			headers["X-Apple-OAuth-Require-Grant-Code"] = "true"
		}
		if f.state.OfferUpgrade {
			headers["X-Apple-Offer-Security-Upgrade"] = "1"
		}
		if appID {
			headers["X-Apple-App-Id"] = f.state.WidgetKey
		}
		if f.state.AuthAttributes != "" {
			headers["X-Apple-Auth-Attributes"] = f.state.AuthAttributes
		}
		if f.state.RepairToken != "" {
			headers["X-Apple-Repair-Session-Token"] = f.state.RepairToken
		}
		if sendHashcash && f.state.HashcashChallenge != "" && f.state.HashcashBits > 0 {
			stamp, err := appleweb.SolveHashcash(f.ctx, f.state.HashcashChallenge, f.state.HashcashBits)
			if err != nil {
				return nil, err
			}
			headers["X-APPLE-HC"] = stamp
		}
	}
	if appleOnboardingURLWithin(rawURL, f.endpoints.AppleID) {
		headers["X-Apple-Widget-Key"] = f.state.WidgetKey
		headers["X-Apple-Skip-Repair-Attributes"] = "[]"
		if f.state.OAuthContext != "" {
			headers["X-Apple-OAuth-Context"] = f.state.OAuthContext
		}
		if f.state.RepairToken != "" {
			headers["X-Apple-Session-Token"] = f.state.RepairToken
		}
	}
	return headers, nil
}

func addAppleOnboardingLegacyClientHints(headers map[string]string, browser appleweb.BrowserProfile) {
	if browser.UserAgent == appleweb.AutomatedUserAgent {
		return
	}
	headers["Sec-CH-UA"] = browser.SecCHUA
	headers["Sec-CH-UA-Mobile"] = "?0"
	headers["Sec-CH-UA-Platform"] = browser.SecCHPlatform
}

func (f *appleOnboardingFlow) absorb(headers http.Header, host string) {
	if value := strings.TrimSpace(headers.Get("scnt")); value != "" {
		f.state.Scnt[host] = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-ID-Session-Id")); value != "" {
		f.state.SessionID = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-Auth-Attributes")); value != "" {
		f.state.AuthAttributes = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-HC-Bits")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			f.state.HashcashBits = parsed
		}
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-HC-Challenge")); value != "" {
		f.state.HashcashChallenge = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-OAuth-Context")); value != "" {
		f.state.OAuthContext = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-GS-Token")); value != "" {
		f.state.GSToken = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-ID-Account-Country")); value != "" {
		f.state.AccountCountry = value
	}
	if value := strings.TrimSpace(headers.Get("X-Apple-Repair-Session-Token")); value != "" {
		f.state.RepairToken = value
	} else if value := strings.TrimSpace(headers.Get("X-Apple-Session-Token")); value != "" {
		f.state.RepairToken = value
	}
	location := strings.TrimSpace(headers.Get("Location"))
	if strings.HasPrefix(location, "/") || strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		f.state.Location = location
	} else {
		f.state.Location = ""
	}
}

func (f *appleOnboardingFlow) authorize() error {
	query := url.Values{
		"frame_id": {f.state.FrameID}, "skVersion": {appleOnboardingSKVersion}, "iframeId": {f.state.FrameID},
		"client_id": {f.state.WidgetKey}, "redirect_uri": {f.state.RedirectURI}, "response_type": {"code"},
		"response_mode": {"web_message"}, "state": {f.state.FrameID}, "authVersion": {f.state.AuthVersion}, "language": {f.state.Locale},
	}
	_, err := f.request(http.MethodGet, strings.TrimRight(f.state.ServiceURL, "/")+"/auth/authorize/signin?"+query.Encode(), nil, true, false, false, false, false, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if err != nil {
		return err
	}
	if f.state.SessionID == "" || f.state.Scnt[appleOnboardingHost(f.state.ServiceURL)] == "" {
		return &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple authorization did not return a session.", Retryable: true}
	}
	return nil
}

func (f *appleOnboardingFlow) signin(email, password string) (map[string]any, error) {
	remember := strconv.FormatBool(f.state.RememberMe)
	federate, err := f.postObject(strings.TrimRight(f.state.ServiceURL, "/")+"/auth/federate?isRememberMeEnabled="+remember, map[string]any{
		"accountName": email, "rememberMe": f.state.RememberMe,
	}, "federate", true, false, false)
	if err != nil {
		return nil, err
	}
	if appleOnboardingBool(federate["federated"]) {
		return nil, &AppleOnboardingError{Category: "federated_account", SafeMessage: "Federated Apple Account login is unsupported."}
	}
	privateA, publicA, err := appleweb.SRPPublicA()
	if err != nil {
		return nil, err
	}
	initResponse, err := f.postObject(strings.TrimRight(f.state.ServiceURL, "/")+"/auth/signin/init", map[string]any{
		"a": base64.StdEncoding.EncodeToString(publicA), "accountName": email, "protocols": []string{"s2k", "s2k_fo"},
	}, "signin/init", true, false, false)
	if err != nil {
		return nil, err
	}
	if f.state.Status != http.StatusOK || appleOnboardingString(initResponse["b"]) == "" || appleOnboardingString(initResponse["c"]) == "" {
		return nil, appleOnboardingPermanent("signin_rejected", "Apple rejected the sign-in request.", initResponse)
	}
	salt, err := base64.StdEncoding.DecodeString(appleOnboardingString(initResponse["salt"]))
	if err != nil {
		return nil, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple sign-in returned invalid SRP data.", Retryable: true}
	}
	serverB, err := base64.StdEncoding.DecodeString(appleOnboardingString(initResponse["b"]))
	if err != nil {
		return nil, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple sign-in returned invalid SRP data.", Retryable: true}
	}
	iterations, err := strconv.Atoi(appleOnboardingString(initResponse["iteration"]))
	if err != nil || iterations < 1 {
		return nil, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple sign-in returned invalid SRP data.", Retryable: true}
	}
	protocol := appleOnboardingString(initResponse["protocol"])
	if protocol == "" {
		protocol = "s2k"
	}
	m1, m2, err := appleweb.SRPProofs(strings.ToLower(email), password, salt, iterations, protocol, privateA, publicA, serverB)
	if err != nil {
		return nil, err
	}
	complete, err := f.postObject(strings.TrimRight(f.state.ServiceURL, "/")+"/auth/signin/complete?isRememberMeEnabled="+remember, map[string]any{
		"accountName": email, "rememberMe": f.state.RememberMe, "m1": base64.StdEncoding.EncodeToString(m1),
		"m2": base64.StdEncoding.EncodeToString(m2), "c": initResponse["c"], "trustTokens": []string{},
	}, "signin/complete", true, false, false)
	if err != nil {
		return nil, err
	}
	if f.state.Status == http.StatusUnauthorized || appleOnboardingServiceError(complete) != "" && f.state.Status != http.StatusConflict && f.state.Status != http.StatusPreconditionFailed {
		category := "invalid_credentials"
		if appleOnboardingLooksLocked(complete) {
			category = "account_locked"
		}
		return nil, &AppleOnboardingError{Category: category, SafeMessage: "Apple Account credentials were rejected.", ProviderMessage: safeICloudImportMessage(appleOnboardingServiceError(complete))}
	}
	return complete, nil
}

func (f *appleOnboardingFlow) submitIDMSAQuestions(secret iCloudOnboardingSecret) error {
	body, err := f.request(http.MethodGet, strings.TrimRight(f.state.ServiceURL, "/")+"/auth", nil, true, false, false, false, false, "text/html")
	if err != nil {
		return err
	}
	questions := appleOnboardingQuestionList(appleOnboardingMap(appleOnboardingMap(parseAppleOnboardingBootArgs(string(body))["twoSV"])["securityQuestions"])["questions"])
	answers, ok := matchAppleOnboardingAnswers(secret.SecurityAnswers, questions)
	if !ok {
		return &AppleOnboardingError{Category: "security_answers_mismatch", SafeMessage: "Stored security answers do not match the Apple security questions."}
	}
	payload := make([]map[string]any, 0, len(questions))
	for index, question := range questions {
		payload = append(payload, map[string]any{"question": question["question"], "answer": answers[index], "id": question["id"], "number": question["number"]})
	}
	verified, err := f.postObject(strings.TrimRight(f.state.ServiceURL, "/")+"/auth/verify/questions", map[string]any{"questions": payload}, "verify/questions", false, false, false)
	if err != nil {
		return err
	}
	if f.state.Status != http.StatusOK && f.state.Status != http.StatusNoContent && f.state.Status != http.StatusConflict && f.state.Status != http.StatusPreconditionFailed {
		return appleOnboardingPermanent("security_answers_rejected", "Apple rejected the stored security answers.", verified)
	}
	return nil
}

func (f *appleOnboardingFlow) submitAppleIDQuestions(secret iCloudOnboardingSecret, questions []map[string]any) error {
	answers, ok := matchAppleOnboardingAnswers(secret.SecurityAnswers, questions)
	if !ok {
		return &AppleOnboardingError{Category: "security_answers_mismatch", SafeMessage: "Stored security answers do not match the Apple security questions."}
	}
	payload := make([]map[string]any, 0, len(questions))
	for index, question := range questions {
		payload = append(payload, map[string]any{"id": question["id"], "question": question["question"], "answer": answers[index]})
	}
	verified, err := f.postObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/verify/questions", map[string]any{"questions": payload}, "account/verify/questions", false, false, false)
	if err != nil {
		return err
	}
	if f.state.Status != http.StatusOK && f.state.Status != http.StatusNoContent {
		return appleOnboardingPermanent("security_answers_rejected", "Apple rejected the stored security answers.", verified)
	}
	return nil
}

func (f *appleOnboardingFlow) prepareTrustedPhone(boundNumber string) error {
	body, err := f.request(http.MethodGet, strings.TrimRight(f.state.ServiceURL, "/")+"/auth", nil, true, false, false, false, false, "text/html")
	if err != nil {
		return err
	}
	boot := parseAppleOnboardingBootArgs(string(body))
	verification := appleOnboardingMap(appleOnboardingMap(boot["twoSV"])["phoneNumberVerification"])
	phones := appleOnboardingTrustedPhones(verification)
	if len(phones) == 0 {
		return &AppleOnboardingError{Category: "trusted_phone_missing", SafeMessage: "Apple did not return the trusted phone number."}
	}
	phone, lastTwo, err := selectAppleOnboardingTrustedPhone(phones, boundNumber)
	if err != nil {
		return err
	}
	phoneID, err := json.Marshal(phone["id"])
	if err != nil || string(phoneID) == "null" {
		return &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple returned an invalid trusted phone.", Retryable: true}
	}
	f.state.PendingTrustedPhoneID = phoneID
	f.state.PendingPhoneLastTwo = lastTwo
	return nil
}

func appleOnboardingTrustedPhones(value any) []any {
	switch typed := value.(type) {
	case map[string]any:
		if phones := appleOnboardingSlice(typed["trustedPhoneNumbers"]); len(phones) > 0 {
			return phones
		}
		if phone := appleOnboardingMap(typed["trustedPhoneNumber"]); len(phone) > 0 {
			return []any{phone}
		}
		for _, nested := range typed {
			if phones := appleOnboardingTrustedPhones(nested); len(phones) > 0 {
				return phones
			}
		}
	case []any:
		for _, nested := range typed {
			if phones := appleOnboardingTrustedPhones(nested); len(phones) > 0 {
				return phones
			}
		}
	}
	return nil
}

func selectAppleOnboardingTrustedPhone(phones []any, boundNumber string) (map[string]any, string, error) {
	boundDigits := appleOnboardingDigits(boundNumber)
	if boundDigits == "" && len(phones) != 1 {
		return nil, "", &AppleOnboardingError{Category: "phone_binding_ambiguous", SafeMessage: "Multiple Apple trusted phones require an explicit bound phone number."}
	}
	var selected map[string]any
	lastTwo := ""
	matches := 0
	for _, value := range phones {
		candidate := appleOnboardingMap(value)
		candidateLastTwo := appleOnboardingTrustedPhoneSuffix(candidate)
		if candidateLastTwo == "" || (boundDigits != "" && !strings.HasSuffix(boundDigits, candidateLastTwo)) {
			continue
		}
		selected = candidate
		lastTwo = candidateLastTwo
		matches++
	}
	if matches == 0 {
		if boundDigits == "" {
			return nil, "", &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple returned an invalid trusted phone.", Retryable: true}
		}
		return nil, "", &AppleOnboardingError{Category: "phone_binding_mismatch", SafeMessage: "The Apple trusted phones do not match the permanently bound phone number."}
	}
	if matches > 1 {
		return nil, "", &AppleOnboardingError{Category: "phone_binding_ambiguous", SafeMessage: "Multiple Apple trusted phones match the permanently bound phone number."}
	}
	return selected, lastTwo, nil
}

func appleOnboardingTrustedPhoneSuffix(phone map[string]any) string {
	for _, key := range []string{"lastTwoDigits", "lastTwo", "phoneNumber", "number", "formattedNumber", "obfuscatedNumber", "maskedNumber"} {
		digits := appleOnboardingDigits(appleOnboardingString(phone[key]))
		if len(digits) >= 2 {
			return digits[len(digits)-2:]
		}
	}
	return ""
}

func (f *appleOnboardingFlow) prepareEnrollment(secret iCloudOnboardingSecret) error {
	upgrade, err := f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/security/upgrade", "security/upgrade", false, false, false, "application/json, text/plain, */*")
	if err != nil {
		return err
	}
	f.rememberCountry(upgrade)
	verification, err := f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/security/upgrade/verification", "upgrade/verification", false, false, false, "application/json, text/plain, */*")
	if err != nil {
		return err
	}
	if f.state.Status == http.StatusUnavailableForLegalReasons {
		questions := appleOnboardingQuestionList(verification["questions"])
		if len(questions) == 0 {
			return &AppleOnboardingError{Category: "security_questions_missing", SafeMessage: "Apple did not return the required security questions."}
		}
		if err := f.submitAppleIDQuestions(secret, questions); err != nil {
			return err
		}
		verification, err = f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/security/upgrade/verification", "upgrade/verification", false, false, false, "application/json, text/plain, */*")
		if err != nil {
			return err
		}
	}
	if f.state.Status != http.StatusOK {
		return appleOnboardingPermanent("phone_enrollment_unavailable", "Apple did not allow trusted phone enrollment.", verification)
	}
	return nil
}

func (f *appleOnboardingFlow) repairOptions() (map[string]any, error) {
	data, err := f.getObject(strings.TrimRight(f.endpoints.AppleID, "/")+"/account/manage/repair/options", "repair/options", false, false, false, "application/json, text/plain, */*")
	if err == nil {
		f.rememberCountry(data)
	}
	return data, err
}

func (f *appleOnboardingFlow) openRepair() error {
	repairURL := f.state.Location
	if strings.HasPrefix(repairURL, "/") {
		repairURL = strings.TrimRight(f.endpoints.AppleID, "/") + repairURL
	}
	if !strings.Contains(repairURL, "widget/account/repair") {
		return nil
	}
	_, err := f.request(http.MethodGet, repairURL, nil, true, false, false, false, false, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	return err
}

func (f *appleOnboardingFlow) completeRepair() error {
	data, err := f.postObject(strings.TrimRight(f.state.ServiceURL, "/")+"/auth/repair/complete", map[string]any{}, "repair/complete", false, false, false)
	if err != nil {
		return err
	}
	if f.state.Status != http.StatusOK && f.state.Status != http.StatusNoContent {
		return appleOnboardingPermanent("repair_incomplete", "Apple Account repair could not be completed.", data)
	}
	if f.state.RepairToken == "" {
		return &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple repair did not return a session token.", Retryable: true}
	}
	return nil
}

func (f *appleOnboardingFlow) hasCookie(name string) (bool, error) {
	urls := []string{f.endpoints.Account, f.endpoints.AppleID, f.endpoints.IDMSA, f.endpoints.ICloud, f.endpoints.Setup}
	cookies, err := f.http.SnapshotCookies(urls...)
	if err != nil {
		return false, err
	}
	for _, cookie := range cookies {
		if cookie.Name == name && cookie.Value != "" {
			return true, nil
		}
	}
	return false, nil
}

func (f *appleOnboardingFlow) rememberCountry(data map[string]any) {
	if f.state.AccountCountry != "" {
		return
	}
	for _, path := range [][]string{{"dsInfo", "countryCode"}, {"account", "countryCode"}, {"appleID", "countryCode"}, {"countryCode"}} {
		var current any = data
		for _, key := range path {
			current = appleOnboardingMap(current)[key]
		}
		if value := appleOnboardingString(current); value != "" {
			f.state.AccountCountry = strings.ToUpper(value)
			return
		}
	}
}

func decodeAppleOnboardingObject(body []byte, label string) (map[string]any, error) {
	if strings.TrimSpace(string(body)) == "" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		return nil, &AppleOnboardingError{Category: "invalid_response", SafeMessage: "Apple returned an invalid " + label + " response.", Retryable: true}
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

func parseAppleOnboardingBootArgs(body string) map[string]any {
	for _, match := range appleOnboardingBootArgsPattern.FindAllStringSubmatch(body, -1) {
		if len(match) != 2 {
			continue
		}
		data, err := decodeAppleOnboardingObject([]byte(match[1]), "boot arguments")
		if err != nil {
			continue
		}
		direct := appleOnboardingMap(data["direct"])
		if len(direct) == 0 {
			direct = data
		}
		twoSV := appleOnboardingMap(direct["twoSV"])
		if len(appleOnboardingMap(twoSV["securityQuestions"])) > 0 || len(appleOnboardingMap(twoSV["phoneNumberVerification"])) > 0 || appleOnboardingString(direct["authType"]) != "" || appleOnboardingString(direct["authInitialRoute"]) != "" || len(appleOnboardingMap(direct["authWidgetConfig"])) > 0 {
			return direct
		}
	}
	return map[string]any{}
}

func matchAppleOnboardingAnswers(entries [3]iCloudSecurityAnswer, questions []map[string]any) ([]string, bool) {
	if len(questions) == 0 {
		return nil, false
	}
	byQuestion := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key := normalizeAppleOnboardingQuestion(entry.Question); key != "" && strings.TrimSpace(entry.Answer) != "" {
			byQuestion[key] = strings.TrimSpace(entry.Answer)
		}
	}
	answers := make([]string, len(questions))
	all := true
	for index, question := range questions {
		answers[index] = byQuestion[normalizeAppleOnboardingQuestion(appleOnboardingString(question["question"]))]
		all = all && answers[index] != ""
	}
	if all {
		return answers, true
	}
	order := map[string]int{"130": 0, "136": 1, "142": 2}
	for index, question := range questions {
		entryIndex, ok := order[appleOnboardingString(question["id"])]
		if !ok || entryIndex >= len(entries) || strings.TrimSpace(entries[entryIndex].Answer) == "" {
			return nil, false
		}
		answers[index] = strings.TrimSpace(entries[entryIndex].Answer)
	}
	return answers, true
}

func appleOnboardingMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func appleOnboardingSlice(value any) []any {
	result, _ := value.([]any)
	return result
}

func appleOnboardingQuestionList(value any) []map[string]any {
	values := appleOnboardingSlice(value)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item := appleOnboardingMap(value); len(item) > 0 {
			result = append(result, item)
		}
	}
	return result
}

func appleOnboardingString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

func appleOnboardingBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func appleOnboardingDigits(value string) string {
	var result strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func normalizeAppleOnboardingQuestion(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func appleOnboardingHost(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
	return parsed.Hostname()
}

func appleOnboardingURLWithin(rawURL, base string) bool {
	raw, rawErr := url.Parse(rawURL)
	root, rootErr := url.Parse(base)
	return rawErr == nil && rootErr == nil && strings.EqualFold(raw.Hostname(), root.Hostname())
}

func appleOnboardingRetryAt(raw string, now time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return now.UTC().Add(time.Duration(min(seconds, 3600)) * time.Second)
	}
	if parsed, err := http.ParseTime(raw); err == nil && parsed.After(now) {
		return parsed.UTC()
	}
	return now.UTC().Add(time.Minute)
}

func appleOnboardingServiceError(data map[string]any) string {
	values := appleOnboardingSlice(data["serviceErrors"])
	if len(values) == 0 {
		values = appleOnboardingSlice(data["service_errors"])
	}
	messages := make([]string, 0, len(values))
	for _, value := range values {
		item := appleOnboardingMap(value)
		message := firstNonEmpty(appleOnboardingString(item["message"]), appleOnboardingString(item["code"]))
		if message != "" {
			messages = append(messages, message)
		}
	}
	return strings.Join(messages, "; ")
}

func appleOnboardingLooksLocked(data map[string]any) bool {
	message := strings.ToLower(appleOnboardingServiceError(data))
	return strings.Contains(message, "locked") || strings.Contains(message, "disabled") || strings.Contains(message, "inactive")
}

func appleOnboardingPermanent(category, message string, data map[string]any) error {
	providerMessage := safeICloudImportMessage(appleOnboardingServiceError(data))
	if appleOnboardingLooksLocked(data) {
		category = "account_locked"
		message = "The Apple Account is locked or disabled."
	}
	return &AppleOnboardingError{Category: category, SafeMessage: message, ProviderMessage: providerMessage}
}

func appleOnboardingRestart(stage string) error {
	return &AppleOnboardingError{Category: "session_expired", SafeMessage: "Apple authentication session expired; sign-in will restart.", RestartStage: stage}
}

func appleOnboardingHTMLAttr(html, name string) string {
	pattern := regexp.MustCompile(fmt.Sprintf(appleOnboardingHTMLAttrPattern.String(), regexp.QuoteMeta(name)))
	match := pattern.FindStringSubmatch(html)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func appleOnboardingAbsoluteURL(current, location string) string {
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		return location
	}
	base, err := url.Parse(current)
	if err != nil {
		return location
	}
	reference, err := url.Parse(location)
	if err != nil {
		return location
	}
	return base.ResolveReference(reference).String()
}

func appleOnboardingExtractInvite(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", &AppleOnboardingError{Category: "family_invite_invalid", SafeMessage: "Family invitation is empty."}
	}
	if !strings.Contains(raw, "://") {
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", &AppleOnboardingError{Category: "family_invite_invalid", SafeMessage: "Family invitation is invalid."}
	}
	token := firstNonEmpty(parsed.Query().Get("inviteCode"), parsed.Query().Get("token"))
	if token == "" {
		return "", &AppleOnboardingError{Category: "family_invite_invalid", SafeMessage: "Family invitation token is missing."}
	}
	return token, nil
}

func appleOnboardingFormatPhone(country, number string) string {
	digits := appleOnboardingDigits(number)
	iso := strings.ToUpper(strings.TrimSpace(country))
	if (iso == "US" || iso == "CA") && len(digits) == 11 && strings.HasPrefix(digits, "1") {
		digits = digits[1:]
	}
	if (iso == "US" || iso == "CA") && len(digits) == 10 {
		return fmt.Sprintf("(%s) %s-%s", digits[:3], digits[3:6], digits[6:])
	}
	return strings.TrimSpace(number)
}

func appleOnboardingServiceBase(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return strings.TrimRight(raw, "/")
	}
	return parsed.Scheme + "://" + parsed.Host
}

func appleOnboardingCookieString(cookies []msacl.SessionCookie, suffix string) string {
	values := make([]string, 0, len(cookies))
	seen := make(map[string]struct{})
	for _, cookie := range cookies {
		domain := strings.ToLower(strings.TrimPrefix(firstNonEmpty(cookie.Domain, cookie.Host), "."))
		if !strings.HasSuffix(domain, suffix) || cookie.Name == "" || cookie.Value == "" {
			continue
		}
		if _, exists := seen[cookie.Name]; exists {
			continue
		}
		seen[cookie.Name] = struct{}{}
		values = append(values, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(values, "; ")
}

func appleOnboardingRawValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, errors.New("missing raw value")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}
