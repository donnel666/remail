package icloud

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/appleweb"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
)

type appleOnboardingScriptedResponse struct {
	status int
	body   string
	header http.Header
}

type appleOnboardingScriptedSession struct {
	responses      []appleOnboardingScriptedResponse
	requests       []string
	requestHeaders []map[string]string
	requestBodies  []any
	cookies        []msacl.SessionCookie
}

func (s *appleOnboardingScriptedSession) Request(method, rawURL string, headers map[string]string, body any, _ bool) (*appleOnboardingHTTPResponse, error) {
	s.requests = append(s.requests, method+" "+rawURL)
	s.requestBodies = append(s.requestBodies, body)
	copyHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		copyHeaders[key] = value
	}
	s.requestHeaders = append(s.requestHeaders, copyHeaders)
	if len(s.responses) == 0 {
		return nil, &AppleOnboardingError{Category: "test_script_empty", SafeMessage: "test script is empty"}
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return &appleOnboardingHTTPResponse{StatusCode: response.status, Body: response.body, URL: rawURL, Header: response.header}, nil
}

func (s *appleOnboardingScriptedSession) SnapshotCookies(...string) ([]msacl.SessionCookie, error) {
	return append([]msacl.SessionCookie(nil), s.cookies...), nil
}

func (s *appleOnboardingScriptedSession) RestoreCookies(cookies []msacl.SessionCookie) error {
	if len(s.cookies) == 0 {
		s.cookies = append([]msacl.SessionCookie(nil), cookies...)
	}
	return nil
}

func appleOnboardingTestClient(now time.Time, session *appleOnboardingScriptedSession) *appleOnboardingClient {
	return &appleOnboardingClient{
		newSession: func(context.Context) (appleOnboardingHTTPSession, error) { return session, nil },
		endpoints:  defaultAppleOnboardingEndpoints(),
		now:        func() time.Time { return now },
	}
}

func appleOnboardingTestState(t *testing.T, mutate func(*appleOnboardingBrowserState)) json.RawMessage {
	t.Helper()
	state := appleOnboardingBrowserState{
		Version: appleOnboardingStateVersion, Mode: "manage", ServiceURL: "https://idmsa.apple.com/appleauth",
		WidgetKey: "widget", RedirectURI: "https://account.apple.com", FrameID: "frame", Locale: "zh_CN",
		Scnt: map[string]string{"idmsa.apple.com": "signin-scnt", "appleid.apple.com": "manage-scnt"},
	}
	phoneID, _ := json.Marshal(7)
	state.PendingTrustedPhoneID = phoneID
	if mutate != nil {
		mutate(&state)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAppleOnboardingNewSessionUsesWindowsFingerprint(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	session := &appleOnboardingScriptedSession{}
	client := appleOnboardingTestClient(now, session)
	flow, err := loadAppleOnboardingFlow(context.Background(), client, nil, "new-account@example.com")
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := appleweb.BrowserProfileForUserAgent(flow.state.UserAgent)
	if !ok || profile.SecCHPlatform != `"Windows"` {
		t.Fatalf("new onboarding profile = %+v", profile)
	}
	headers, err := flow.headers("https://appleid.apple.com/account/manage", false, true, false, false, false, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if headers["User-Agent"] != profile.UserAgent || headers["Accept-Language"] != appleOnboardingLanguage {
		t.Fatalf("request did not use the selected profile: %v", headers)
	}
	for _, name := range []string{"Sec-CH-UA", "Sec-CH-UA-Mobile", "Sec-CH-UA-Platform"} {
		if headers[name] != "" {
			t.Fatalf("new onboarding request unexpectedly sent %s: %v", name, headers)
		}
	}
	var fd struct {
		UserAgent string `json:"U"`
	}
	if err := json.Unmarshal([]byte(headers["X-Apple-I-FD-Client-Info"]), &fd); err != nil || fd.UserAgent != profile.UserAgent {
		t.Fatalf("FD fingerprint mismatch: payload=%+v err=%v", fd, err)
	}

	snapshot, err := flow.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadAppleOnboardingFlow(context.Background(), client, snapshot, "different@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.UserAgent != profile.UserAgent {
		t.Fatalf("persisted profile changed: got %q want %q", reloaded.state.UserAgent, profile.UserAgent)
	}
	if err := reloaded.reset("manage"); err != nil || reloaded.state.UserAgent != profile.UserAgent {
		t.Fatalf("reset changed profile: userAgent=%q err=%v", reloaded.state.UserAgent, err)
	}
}

func TestAppleOnboardingFamilyResetDropsPreviousCookies(t *testing.T) {
	oldSession := &appleOnboardingScriptedSession{}
	familySession := &appleOnboardingScriptedSession{}
	sessions := []appleOnboardingHTTPSession{oldSession, familySession}
	client := &appleOnboardingClient{
		newSession: func(context.Context) (appleOnboardingHTTPSession, error) {
			session := sessions[0]
			sessions = sessions[1:]
			return session, nil
		},
		endpoints: defaultAppleOnboardingEndpoints(),
		now:       time.Now,
	}
	state := appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
		state.Cookies = []msacl.SessionCookie{{Name: "old", Value: "cookie", Domain: ".apple.com", Host: "www.icloud.com"}}
	})
	flow, err := loadAppleOnboardingFlow(context.Background(), client, state, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.reset("family"); err != nil {
		t.Fatal(err)
	}
	if flow.http != familySession || len(familySession.cookies) != 0 {
		t.Fatalf("family invitation reused prior cookies: %#v", familySession.cookies)
	}
}

func TestParseAppleOnboardingBootArgsByID(t *testing.T) {
	body := `<script type="application/json" id="boot_args">{"direct":{"authWidgetConfig":{"serviceKey":"family-key"}}}</script>`
	if got := appleOnboardingString(appleOnboardingMap(parseAppleOnboardingBootArgs(body)["authWidgetConfig"])["serviceKey"]); got != "family-key" {
		t.Fatalf("service key = %q", got)
	}
}

func TestAppleOnboardingKeepsICloudAndAccountHeadersSeparate(t *testing.T) {
	flow, err := loadAppleOnboardingFlow(context.Background(), appleOnboardingTestClient(time.Now(), &appleOnboardingScriptedSession{}), appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
		state.Mode = "family"
		state.InviteToken = "invite-token"
		state.GSToken = "gs-token"
	}), "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	icloudHeaders, err := flow.headers("https://setup.icloud.com/setup/ws/1/validate", false, false, false, false, false, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if icloudHeaders["Origin"] != flow.endpoints.ICloud || icloudHeaders["Referer"] != strings.TrimRight(flow.endpoints.ICloud, "/")+"/" {
		t.Fatalf("setup.icloud.com used the wrong browser origin: %#v", icloudHeaders)
	}
	if icloudHeaders["X-Apple-GS-Token"] != "" {
		t.Fatalf("iCloud request leaked the family GS token: %#v", icloudHeaders)
	}

	familyHeaders, err := flow.headers("https://account.apple.com/family/invite/details?token=invite-token", false, false, false, false, true, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	wantFamilyReferer := strings.TrimRight(flow.endpoints.Account, "/") + "/family/invite?token=invite-token"
	if familyHeaders["Origin"] != flow.endpoints.Account || familyHeaders["Referer"] != wantFamilyReferer || familyHeaders["X-Apple-GS-Token"] != "gs-token" {
		t.Fatalf("account.apple.com family request used the wrong headers: %#v", familyHeaders)
	}

	manageHeaders, err := flow.headers("https://appleid.apple.com/account/manage", false, true, false, false, true, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if manageHeaders["Origin"] != flow.endpoints.Account || manageHeaders["Referer"] != strings.TrimRight(flow.endpoints.Account, "/")+"/" || manageHeaders["X-Apple-GS-Token"] != "" {
		t.Fatalf("appleid.apple.com management request used the wrong headers: %#v", manageHeaders)
	}
}

func TestAppleOnboardingInvalidStoredSessionRestartsAtSafeStage(t *testing.T) {
	tests := []struct {
		operation string
		purpose   string
		stage     string
	}{
		{appleOnboardingFinishICloud, "", "icloud_prepare"},
		{appleOnboardingFinishICloudCookie, "", "icloud_cookie_prepare"},
		{appleOnboardingJoinFamily, "", "family_reconcile_prepare"},
		{appleOnboardingFetchManage, "", "manage_prepare"},
		{appleOnboardingSendSMS, appleSMSManageLogin, "manage_prepare"},
	}
	for _, test := range tests {
		t.Run(test.operation+test.purpose, func(t *testing.T) {
			session := &appleOnboardingScriptedSession{}
			_, err := appleOnboardingTestClient(time.Now(), session).Execute(context.Background(), AppleOnboardingRequest{
				Operation: test.operation, SMSPurpose: test.purpose, Session: json.RawMessage(`{"version":0}`),
			})
			providerErr, ok := err.(*AppleOnboardingError)
			if !ok || providerErr.Category != "invalid_session" || providerErr.RestartStage != test.stage || len(session.requests) != 0 {
				t.Fatalf("invalid session recovery = %#v requests=%v", err, session.requests)
			}
		})
	}
}

func TestAppleOnboardingMissingPostWriteCookiesRestartsAuthentication(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	t.Run("family", func(t *testing.T) {
		flow, err := loadAppleOnboardingFlow(context.Background(), appleOnboardingTestClient(now, &appleOnboardingScriptedSession{}), appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
			state.Mode = "family"
		}), "test@example.com")
		if err != nil {
			t.Fatal(err)
		}
		_, err = flow.familyJoinResponse()
		providerErr, ok := err.(*AppleOnboardingError)
		if !ok || providerErr.RestartStage != "family_reconcile_prepare" {
			t.Fatalf("family cookie recovery = %#v", err)
		}
	})
	t.Run("manage", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: `{}`}}}
		flow, err := loadAppleOnboardingFlow(context.Background(), appleOnboardingTestClient(now, session), appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
			state.Mode = "manage"
			state.APIKey = "api-key"
		}), "test@example.com")
		if err != nil {
			t.Fatal(err)
		}
		_, err = flow.exportChannels(AppleOnboardingRequest{})
		providerErr, ok := err.(*AppleOnboardingError)
		if !ok || providerErr.RestartStage != "manage_prepare" {
			t.Fatalf("manage cookie recovery = %#v", err)
		}
	})
}

func TestAppleOnboardingSendSMSAndClassifiesFailures(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	t.Run("sent", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: `{}`}}}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingSendSMS, SMSPurpose: appleSMSManageLogin, Session: appleOnboardingTestState(t, nil),
		})
		if err != nil || len(response.Session) == 0 || len(session.requests) != 1 || !strings.Contains(session.requests[0], "/auth/verify/phone") {
			t.Fatalf("unexpected send result: response=%+v requests=%v err=%v", response, session.requests, err)
		}
	})

	t.Run("rate_limited", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{
			status: http.StatusTooManyRequests, body: `{}`, header: http.Header{"Retry-After": []string{"120"}},
		}}}
		_, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingSendSMS, SMSPurpose: appleSMSManageLogin, Session: appleOnboardingTestState(t, nil),
		})
		providerErr, ok := err.(*AppleOnboardingError)
		if !ok || !providerErr.Retryable || providerErr.HTTPStatus != http.StatusTooManyRequests || providerErr.RetryAt == nil || !providerErr.RetryAt.Equal(now.Add(2*time.Minute)) {
			t.Fatalf("unexpected rate-limit error: %#v", err)
		}
	})

	t.Run("rejected_with_diagnostics", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{
			status: http.StatusBadRequest, body: `{"serviceErrors":[{"code":"-28001","message":"Too many verification requests"}]}`,
		}}}
		_, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingSendSMS, SMSPurpose: appleSMSManageLogin, Session: appleOnboardingTestState(t, nil),
		})
		providerErr, ok := err.(*AppleOnboardingError)
		if !ok || !providerErr.SendRejected || providerErr.HTTPStatus != http.StatusBadRequest || providerErr.ProviderMessage != "Too many verification requests" {
			t.Fatalf("unexpected rejected error: %#v", err)
		}
	})

	t.Run("session_expired", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusUnauthorized, body: `{}`}}}
		_, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingSendSMS, SMSPurpose: appleSMSManageLogin, Session: appleOnboardingTestState(t, nil),
		})
		providerErr, ok := err.(*AppleOnboardingError)
		if !ok || providerErr.RestartStage != "manage_prepare" {
			t.Fatalf("unexpected expired-session error: %#v", err)
		}
	})
}

func TestAppleOnboardingRejectsBadCodeAndMismatchedPermanentPhone(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{
		status: http.StatusOK, body: `{"securityCode":{"valid":false}}`,
	}}}
	_, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
		Operation: appleOnboardingVerifySMS, SMSPurpose: appleSMSManageLogin, Code: "123456", Session: appleOnboardingTestState(t, nil),
	})
	providerErr, ok := err.(*AppleOnboardingError)
	if !ok || !providerErr.CodeRejected {
		t.Fatalf("unexpected code error: %#v", err)
	}
	if len(session.requestHeaders) != 1 || session.requestHeaders[0]["X-Apple-App-Id"] != "widget" {
		t.Fatalf("HSA2 code verification did not send the widget app id: %#v", session.requestHeaders)
	}

	boot := `<script type="application/json" class="boot_args">{"direct":{"twoSV":{"phoneNumberVerification":{"trustedPhoneNumbers":[{"id":1,"lastTwoDigits":"99"}]}}}}</script>`
	session = &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: boot}}}
	flow, err := loadAppleOnboardingFlow(context.Background(), appleOnboardingTestClient(now, session), appleOnboardingTestState(t, nil), "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	err = flow.prepareTrustedPhone("15550000034")
	providerErr, ok = err.(*AppleOnboardingError)
	if !ok || providerErr.Category != "phone_binding_mismatch" {
		t.Fatalf("unexpected phone mismatch: %#v", err)
	}
}

func TestAppleOnboardingReconcilesAppliedWritesBeforeMutation(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)

	t.Run("family_expired_session_uses_reconcile_login", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{}
		_, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingJoinFamily,
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.Mode = "family"
				state.InviteToken = "invite-token"
			}),
		})
		providerErr, ok := err.(*AppleOnboardingError)
		if !ok || providerErr.RestartStage != "family_reconcile_prepare" || len(session.requests) != 0 {
			t.Fatalf("unexpected family restart: err=%#v requests=%v", err, session.requests)
		}
	})

	t.Run("family_new_invite_uses_family_token_before_accept", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{
			responses: []appleOnboardingScriptedResponse{
				{status: http.StatusOK, body: `{}`, header: http.Header{"X-Apple-Gs-Token": {"gs-token"}}},
				{status: http.StatusOK, body: `{"final":true}`},
			},
			cookies: []msacl.SessionCookie{{Name: "myacinfo", Value: "active", Domain: ".apple.com", Host: "appleid.apple.com"}},
		}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingJoinFamily,
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.Mode = "family"
				state.InviteToken = "invite-token"
			}),
		})
		if err != nil || response.Next != "ready" || len(session.requests) != 2 ||
			!strings.Contains(session.requests[0], "/family/invite/gs/ws/token") ||
			!strings.Contains(session.requests[1], "/family/invite/accept/familysharing") {
			t.Fatalf("family join response=%+v requests=%v err=%v", response, session.requests, err)
		}
	})

	t.Run("forward_add_already_pending", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{
			status: http.StatusOK,
			body:   `{"account":{"person":{"reachableAtOptions":{"alternateEmailAddresses":[{"address":"relay@example.com","vetted":false,"verificationId":"verify-1"}]}}}}`,
		}}}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingAddForward, ForwardToEmail: "relay@example.com",
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" }),
		})
		if err != nil || response.Next != "pending" || len(session.requests) != 1 || !strings.HasPrefix(session.requests[0], http.MethodGet+" ") {
			t.Fatalf("forward add reconcile response=%+v requests=%v err=%v", response, session.requests, err)
		}
		var state appleOnboardingBrowserState
		if err := json.Unmarshal(response.Session, &state); err != nil || state.ForwardVerificationID != "verify-1" {
			t.Fatalf("forward verification state=%+v err=%v", state, err)
		}
	})

	t.Run("forward_verify_already_vetted", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{
			status: http.StatusOK,
			body:   `{"account":{"person":{"reachableAtOptions":{"alternateEmailAddresses":[{"address":"relay@example.com","vetted":true}]}}}}`,
		}}}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingVerifyForward, ForwardToEmail: "relay@example.com", ForwardCode: "123456",
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.APIKey = "api-key"
				state.ForwardVerificationID = "verify-1"
			}),
		})
		if err != nil || response.Next != "ready" || len(session.requests) != 1 || !strings.HasPrefix(session.requests[0], http.MethodGet+" ") {
			t.Fatalf("forward verify reconcile response=%+v requests=%v err=%v", response, session.requests, err)
		}
	})

	t.Run("forward_verify_uses_saved_id_before_profile_propagates", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
			{status: http.StatusOK, body: `{"account":{"person":{"reachableAtOptions":{"alternateEmailAddresses":[]}}}}`},
			{status: http.StatusOK, body: `{"vettingStatus":{"vetted":true}}`},
		}}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingVerifyForward, ForwardToEmail: "relay@example.com", ForwardCode: "123456",
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.APIKey = "api-key"
				state.ForwardVerificationID = "verify-1"
			}),
		})
		if err != nil || response.Next != "ready" || len(session.requests) != 2 || !strings.HasPrefix(session.requests[1], http.MethodPut+" ") {
			t.Fatalf("forward verify with saved id response=%+v requests=%v err=%v", response, session.requests, err)
		}
	})

	t.Run("forward_verify_keeps_id_from_add_response", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
			{status: http.StatusOK, body: `{"account":{"person":{"reachableAtOptions":{"alternateEmailAddresses":[{"address":"relay@example.com","vetted":false,"verificationId":"stale-id"}]}}}}`},
			{status: http.StatusOK, body: `{"vettingStatus":{"vetted":true}}`},
		}}
		_, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingVerifyForward, ForwardToEmail: "relay@example.com", ForwardCode: "123456",
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.APIKey = "api-key"
				state.ForwardVerificationID = "add-response-id"
			}),
		})
		verification := appleOnboardingMap(appleOnboardingMap(session.requestBodies[1])["verificationInfo"])
		if err != nil || appleOnboardingString(verification["id"]) != "add-response-id" {
			t.Fatalf("forward verification request=%v err=%v", session.requests, err)
		}
	})
}

func TestAppleOnboardingSelectsUniquePermanentlyBoundTrustedPhone(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		phones       string
		wantID       string
		wantCategory string
	}{
		{name: "second phone", phones: `[{"id":1,"lastTwoDigits":"99"},{"id":2,"lastTwoDigits":"34"}]`, wantID: "2"},
		{name: "no match", phones: `[{"id":1,"lastTwoDigits":"99"},{"id":2,"lastTwoDigits":"88"}]`, wantCategory: "phone_binding_mismatch"},
		{name: "ambiguous", phones: `[{"id":1,"lastTwoDigits":"34"},{"id":2,"lastTwoDigits":"34"}]`, wantCategory: "phone_binding_ambiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			boot := `<script type="application/json" class="boot_args">{"direct":{"twoSV":{"phoneNumberVerification":{"trustedPhoneNumbers":` + test.phones + `}}}}</script>`
			session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: boot}}}
			flow, err := loadAppleOnboardingFlow(context.Background(), appleOnboardingTestClient(now, session), appleOnboardingTestState(t, nil), "test@example.com")
			if err != nil {
				t.Fatal(err)
			}
			err = flow.prepareTrustedPhone("15550000034")
			if test.wantCategory != "" {
				providerErr, ok := err.(*AppleOnboardingError)
				if !ok || providerErr.Category != test.wantCategory {
					t.Fatalf("error = %#v, want category %q", err, test.wantCategory)
				}
				return
			}
			if err != nil || string(flow.state.PendingTrustedPhoneID) != test.wantID || flow.state.PendingPhoneLastTwo != "34" {
				t.Fatalf("selected phone id=%s suffix=%q err=%v", flow.state.PendingTrustedPhoneID, flow.state.PendingPhoneLastTwo, err)
			}
		})
	}
}

func TestAppleOnboardingFetchManageDoesNotRepeatTrustedPhoneValidation(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		phones      string
		boundNumber string
		wantLastTwo string
	}{
		{name: "unique", phones: `[{"lastTwoDigits":"34"}]`, wantLastTwo: "34"},
		{name: "matches permanent number", phones: `[{"number":"••••99"},{"maskedNumber":"(***) ***-**34"}]`, boundNumber: "14155550034", wantLastTwo: "34"},
		{name: "ambiguous", phones: `[{"lastTwoDigits":"34"},{"lastTwoDigits":"99"}]`},
		{name: "missing", phones: `[]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{
				{status: http.StatusOK, body: `{}`},
				{status: http.StatusOK, body: `{"apiKey":"api","countryCode":"US","security":{"trustedPhoneNumbers":` + test.phones + `}}`},
			}}
			response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
				Operation: appleOnboardingFetchManage, PhoneNumber: test.boundNumber, Session: appleOnboardingTestState(t, nil),
			})
			if err != nil || response.TrustedPhoneLastTwo != test.wantLastTwo || response.CountryCode != "US" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestAppleOnboardingExportsNewAndPreservedOldChannels(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	old := &AppleOnboardingChannel{Kind: iCloudChannelWeb, Host: "p1-maildomainws.icloud.com", Cookie: "old=value"}
	session := &appleOnboardingScriptedSession{
		responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: `{}`}},
		cookies:   []msacl.SessionCookie{{Name: "myacinfo", Value: "new-value", Domain: ".apple.com", Host: "appleid.apple.com"}},
	}
	response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
		Operation: appleOnboardingExport,
		Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
			state.APIKey = "api-key"
			state.OldChannel = old
			state.AccountCountry = "US"
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.NewChannel == nil || !strings.Contains(response.NewChannel.Cookie, "myacinfo=new-value") || response.NewChannel.APIKey != "api-key" {
		t.Fatalf("unexpected new channel: %+v", response.NewChannel)
	}
	if response.OldChannel == nil || response.OldChannel.Cookie != old.Cookie || response.CountryCode != "US" {
		t.Fatalf("old channel or country was not preserved: %+v", response)
	}
}

func TestAppleOnboardingExportSetsDefaultForwardingAddress(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	session := &appleOnboardingScriptedSession{
		responses: []appleOnboardingScriptedResponse{
			{status: http.StatusOK, body: `{}`},
			{status: http.StatusOK, body: `{}`},
		},
		cookies: []msacl.SessionCookie{{Name: "myacinfo", Value: "new-value", Domain: ".apple.com", Host: "appleid.apple.com"}},
	}
	response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
		Operation:      appleOnboardingExport,
		ForwardToEmail: "Relay@Example.com",
		Session:        appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) { state.APIKey = "api-key" }),
	})
	if err != nil || response.NewChannel == nil {
		t.Fatalf("export response=%+v err=%v", response, err)
	}
	if len(session.requests) != 2 || !strings.HasSuffix(session.requests[0], "/account/manage/forwardemail") || !strings.HasSuffix(session.requests[1], "/account/manage/email/private") {
		t.Fatalf("export request order=%v", session.requests)
	}
	payload, ok := session.requestBodies[0].(map[string]any)
	if !ok || payload["forwardToEmail"] != "relay@example.com" {
		t.Fatalf("default forwarding payload=%#v", session.requestBodies[0])
	}
}
