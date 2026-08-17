package icloud

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
)

type appleOnboardingScriptedResponse struct {
	status int
	body   string
	header http.Header
}

type appleOnboardingScriptedSession struct {
	responses []appleOnboardingScriptedResponse
	requests  []string
	cookies   []msacl.SessionCookie
}

func (s *appleOnboardingScriptedSession) Request(method, rawURL string, _ map[string]string, _ any, _ bool) (*appleOnboardingHTTPResponse, error) {
	s.requests = append(s.requests, method+" "+rawURL)
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
		if !ok || !providerErr.Retryable || providerErr.RetryAt == nil || !providerErr.RetryAt.Equal(now.Add(2*time.Minute)) {
			t.Fatalf("unexpected rate-limit error: %#v", err)
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

	boot := `<script type="application/json" class="boot_args">{"direct":{"twoSV":{"phoneNumberVerification":{"trustedPhoneNumbers":[{"id":1,"lastTwoDigits":"99"}]}}}}</script>`
	session = &appleOnboardingScriptedSession{responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: boot}}}
	flow, err := loadAppleOnboardingFlow(context.Background(), appleOnboardingTestClient(now, session), appleOnboardingTestState(t, nil))
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

	t.Run("family_already_joined", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{
			responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: `{"account":{"person":{"familyMembership":{"status":"active"}}}}`}},
			cookies:   []msacl.SessionCookie{{Name: "myacinfo", Value: "active", Domain: ".apple.com", Host: "appleid.apple.com"}},
		}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingJoinFamily,
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.Mode = "family"
				state.InviteToken = "invite-token"
			}),
		})
		if err != nil || response.Next != "ready" || len(session.requests) != 1 || !strings.Contains(session.requests[0], "/account/manage") {
			t.Fatalf("family reconcile response=%+v requests=%v err=%v", response, session.requests, err)
		}
	})

	t.Run("family_joined_with_different_organizer_identifier", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{
			responses: []appleOnboardingScriptedResponse{{status: http.StatusOK, body: `{"account":{"person":{"familyMembership":{"status":"active","organizer":{"appleId":"other@example.com"}}}}}`}},
			cookies:   []msacl.SessionCookie{{Name: "myacinfo", Value: "active", Domain: ".apple.com", Host: "appleid.apple.com"}},
		}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingJoinFamily,
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.Mode = "family"
				state.InviteToken = "invite-token"
				state.FamilyOrganizerEmail = "primary@example.com"
			}),
		})
		if err != nil || response.Next != "ready" || len(session.requests) != 1 {
			t.Fatalf("family reconcile response=%+v requests=%v err=%v", response, session.requests, err)
		}
	})

	t.Run("family_invite_accepted_with_different_organizer_identifier", func(t *testing.T) {
		session := &appleOnboardingScriptedSession{
			responses: []appleOnboardingScriptedResponse{
				{status: http.StatusOK, body: `{}`},
				{status: http.StatusOK, body: `{"status":"accepted","organizer":{"appleId":"other@example.com"}}`},
			},
			cookies: []msacl.SessionCookie{{Name: "myacinfo", Value: "active", Domain: ".apple.com", Host: "appleid.apple.com"}},
		}
		response, err := appleOnboardingTestClient(now, session).Execute(context.Background(), AppleOnboardingRequest{
			Operation: appleOnboardingJoinFamily,
			Session: appleOnboardingTestState(t, func(state *appleOnboardingBrowserState) {
				state.Mode = "family"
				state.InviteToken = "invite-token"
				state.FamilyOrganizerEmail = "primary@example.com"
			}),
		})
		if err != nil || response.Next != "ready" || len(session.requests) != 2 {
			t.Fatalf("family invite reconcile response=%+v requests=%v err=%v", response, session.requests, err)
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
			flow, err := loadAppleOnboardingFlow(context.Background(), appleOnboardingTestClient(now, session), appleOnboardingTestState(t, nil))
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

func TestAppleOnboardingFetchManageSelectsUniqueTrustedPhone(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		phones       string
		boundNumber  string
		wantLastTwo  string
		wantCategory string
	}{
		{name: "unique", phones: `[{"lastTwoDigits":"34"}]`, wantLastTwo: "34"},
		{name: "matches permanent number", phones: `[{"number":"••••99"},{"maskedNumber":"(***) ***-**34"}]`, boundNumber: "14155550034", wantLastTwo: "34"},
		{name: "ambiguous without permanent number", phones: `[{"lastTwoDigits":"34"},{"lastTwoDigits":"99"}]`, wantCategory: "phone_binding_ambiguous"},
		{name: "missing", phones: `[]`, wantCategory: "phone_binding_missing"},
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
			if test.wantCategory != "" {
				providerErr, ok := err.(*AppleOnboardingError)
				if !ok || providerErr.Category != test.wantCategory {
					t.Fatalf("unexpected error: %#v", err)
				}
				return
			}
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
