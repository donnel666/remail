package icloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestHMEListBuildsSafeRequestAndMergesSetCookie(t *testing.T) {
	const cookie = "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	var seen *http.Request
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		seen = request
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"X-APPLE-WEBAUTH-TOKEN=rotated; Path=/"}},
			Body: io.NopCloser(strings.NewReader(`{"success":true,"result":{"selectedForwardTo":"icloud@aishop6.com","hmeEmails":[{"hme":"abc@icloud.com","anonymousId":"anon-1","label":"label","note":"note","forwardToEmail":"icloud@aishop6.com","domain":"icloud.com","recipientMailId":"mail-1","isActive":true,"createTimestamp":1700000000000}]}}`)),
		}, nil
	})})
	result, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if seen == nil || seen.URL.Path != "/v2/hme/list" || seen.URL.Query().Get("dsid") != "123" || seen.Header.Get("Cookie") != cookie {
		t.Fatalf("unexpected HME request: %#v", seen)
	}
	if len(result.Aliases) != 1 || result.Aliases[0].Email != "abc@icloud.com" || !result.Aliases[0].Active {
		t.Fatalf("unexpected HME result: %#v", result)
	}
	if !strings.Contains(result.UpdatedCookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
		t.Fatalf("Set-Cookie was not merged: %q", result.UpdatedCookie)
	}
}

func TestHMERefreshSessionRotatesCookiesAndDiscoversCurrentPod(t *testing.T) {
	const cookie = "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Host != "setup.icloud.com.cn" || request.URL.Path != "/setup/ws/1/validate" {
			t.Fatalf("unexpected refresh endpoint: %s %s", request.Method, request.URL.String())
		}
		query := request.URL.Query()
		if query.Get("clientId") != "client" || query.Get("clientBuildNumber") != "build" ||
			query.Get("clientMasteringNumber") != "mastering" || query.Get("requestId") == "" || query.Get("dsid") != "" {
			t.Fatalf("unexpected refresh query: %v", query)
		}
		if request.Header.Get("Cookie") != cookie || request.Header.Get("Origin") != "https://www.icloud.com.cn" {
			t.Fatalf("unexpected refresh headers: %#v", request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{"Set-Cookie": {
				"X-APPLE-WEBAUTH-TOKEN=rotated; Domain=.icloud.com.cn; Path=/",
				"setup-only=value; Path=/setup",
			}},
			Body: io.NopCloser(strings.NewReader(
				`{"dsInfo":{"dsid":123},"webservices":{"premiummailsettings":{"url":"https://p216-maildomainws.icloud.com.cn/"}}}`,
			)),
		}, nil
	})})
	refreshed, err := client.refreshSession(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com.cn", DSID: "123", ClientID: "client",
		ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie, SetupCookie: cookie,
	})
	if err != nil {
		t.Fatalf("refresh session: %v", err)
	}
	if refreshed.Host != "p216-maildomainws.icloud.com.cn" || !strings.Contains(refreshed.Cookie, "X-APPLE-WEBAUTH-TOKEN=rotated") ||
		strings.Contains(refreshed.Cookie, "setup-only=value") || !strings.Contains(refreshed.SetupCookie, "setup-only=value") {
		t.Fatalf("unexpected refreshed session: %#v", refreshed)
	}
}

func TestHMERefreshSessionRejectsAnotherAccountWithoutReturningRotatedCookies(t *testing.T) {
	const cookie = "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Set-Cookie": {"X-APPLE-WEBAUTH-TOKEN=wrong-account; Domain=.icloud.com; Path=/"}},
			Body: io.NopCloser(strings.NewReader(
				`{"dsInfo":{"dsid":"456"},"webservices":{"premiummailsettings":{"url":"https://p216-maildomainws.icloud.com"}}}`,
			)),
		}, nil
	})})
	_, err := client.refreshSession(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client",
		ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie, SetupCookie: cookie,
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "session_invalid" || providerErr.Stage != "validate" ||
		providerErr.UpdatedCookie != "" || providerErr.UpdatedSetupCookie != "" {
		t.Fatalf("mismatched account error = %#v", err)
	}
}

func TestMergeICloudCookiesKeepsOneEntryAfterDeleteAndReplace(t *testing.T) {
	got := mergeICloudCookies("session=old; other=value", []*http.Cookie{
		{Name: "session", MaxAge: -1},
		{Name: "session", Value: "new"},
	})
	if got != "session=new; other=value" {
		t.Fatalf("merged Cookie = %q", got)
	}
}

func TestHMEListDoesNotExposeSessionMaterialOnUnauthorized(t *testing.T) {
	const secret = "session-secret-value"
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(secret))}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	if err == nil {
		t.Fatal("List() error = nil, want unauthorized error")
	}
	var providerErr *hmeError
	if !errors.As(err, &providerErr) || providerErr.Stage != "list" || providerErr.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("missing safe request diagnostics: %#v", err)
	}
	message := err.Error()
	if strings.Contains(message, secret) || strings.Contains(message, "icloud.com") || strings.Contains(message, "token") {
		t.Fatalf("unsafe HME error: %q", message)
	}
}

func TestHMEListKeepsSafeCookieRotationOnProviderError(t *testing.T) {
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Set-Cookie": {"X-APPLE-WEBAUTH-TOKEN=rotated; Path=/"}},
			Body:       io.NopCloser(strings.NewReader("provider-secret")),
		}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	providerErr, ok := err.(*hmeError)
	if !ok || !strings.Contains(providerErr.UpdatedCookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
		t.Fatalf("expected rotated cookie on safe provider error, err=%#v", err)
	}
	if strings.Contains(providerErr.Error(), "provider-secret") {
		t.Fatalf("provider body leaked through error: %q", providerErr.Error())
	}
}

func TestHMEListRejectsRotationThatRemovesCoreSessionCookie(t *testing.T) {
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Set-Cookie": {"X-APPLE-WEBAUTH-TOKEN=; Max-Age=0; Path=/"}},
			Body:       io.NopCloser(strings.NewReader(`{"success":true,"result":{"hmeEmails":[]}}`)),
		}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "session_invalid" || providerErr.UpdatedCookie != "" {
		t.Fatalf("expected invalid rotated session, err=%#v", err)
	}
}

func TestHMEListRejectsOversizedRotatedCookie(t *testing.T) {
	base := "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	cookie := base + "; filler=" + strings.Repeat("x", iCloudImportCookieMaxBytes-len(base)-len("; filler=")-1)
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Set-Cookie": {"rotated=value; Path=/"}},
			Body:       io.NopCloser(strings.NewReader(`{"success":true,"result":{"hmeEmails":[]}}`)),
		}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "provider_response" || providerErr.UpdatedCookie != "" {
		t.Fatalf("expected safe oversized-cookie error, err=%#v", err)
	}
}

func TestHMEListRejectsOversizedAliasField(t *testing.T) {
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		body := `{"success":true,"result":{"hmeEmails":[{"hme":"alias@icloud.com","anonymousId":"id","label":"` + strings.Repeat("x", iCloudHMELabelMaxLength+1) + `","isActive":true}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "provider_response" || !strings.Contains(providerErr.Error(), "invalid alias") {
		t.Fatalf("expected safe oversized-alias error, err=%#v", err)
	}
}

func TestHMEListRejectsMissingAliasSnapshot(t *testing.T) {
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"success":true,"result":{"selectedForwardTo":"icloud@aishop6.com"}}`,
		))}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "snapshot_incomplete" {
		t.Fatalf("missing alias snapshot must be rejected, err=%#v", err)
	}
}

func TestHMEListFollowsContinuationAndRequiresCompleteTotal(t *testing.T) {
	const cookie = "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	calls := 0
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		body := `{"success":true,"result":{"selectedForwardTo":"icloud@aishop6.com","total":2,"hasMore":true,"nextPageToken":"page-2","hmeEmails":[{"hme":"one@icloud.com","anonymousId":"one","isActive":true}]}}`
		if request.URL.Query().Get("nextPageToken") == "page-2" {
			body = `{"success":true,"result":{"total":2,"hasMore":false,"hmeEmails":[{"hme":"two@icloud.com","anonymousId":"two","isActive":true}]}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	result, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	if err != nil || !result.Complete || len(result.Aliases) != 2 || calls != 2 {
		t.Fatalf("paged list: result=%#v calls=%d err=%v", result, calls, err)
	}
	if result.Aliases[1].Email != "two@icloud.com" {
		t.Fatalf("unexpected second page alias: %#v", result.Aliases[1])
	}

	incomplete := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"success":true,"result":{"total":2,"hasMore":false,"hmeEmails":[{"hme":"one@icloud.com","anonymousId":"one","isActive":true}]}}`,
		))}, nil
	})})
	_, err = incomplete.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "snapshot_incomplete" {
		t.Fatalf("incomplete total must be rejected, err=%#v", err)
	}
}

func TestHMEListRejectsContradictoryContinuationMetadata(t *testing.T) {
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"success":true,"result":{"selectedForwardTo":"icloud@aishop6.com","hasMore":false,"nextPageToken":"page-2","hmeEmails":[]}}`,
		))}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "snapshot_incomplete" {
		t.Fatalf("contradictory continuation metadata must be rejected, err=%#v", err)
	}
}

func TestHMEListCarriesEarlierPageCookieAcrossTransportFailure(t *testing.T) {
	const cookie = "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	calls := 0
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("network unavailable")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Set-Cookie": {"X-APPLE-WEBAUTH-TOKEN=rotated; Path=/"}},
			Body: io.NopCloser(strings.NewReader(
				`{"success":true,"result":{"total":2,"hasMore":true,"nextPageToken":"page-2","hmeEmails":[{"hme":"one@icloud.com","anonymousId":"one","isActive":true}]}}`,
			)),
		}, nil
	})})
	_, err := client.list(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	providerErr, ok := err.(*hmeError)
	if !ok || !strings.Contains(providerErr.UpdatedCookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
		t.Fatalf("earlier page cookie must survive a later transport error: calls=%d err=%#v", calls, err)
	}
}

func TestHMERateLimitUsesResponseBodyRetryAfter(t *testing.T) {
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"success":false,"code":-41015,"retryAfter":361.2}`)),
		}, nil
	})})
	_, _, err := client.Generate(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "rate_limited" || providerErr.RetryAfter != 362*time.Second {
		t.Fatalf("rate-limit response = %#v, want retry after 362s", err)
	}
}

func TestAppleAccountRateLimitUsesNestedResponseBodyRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 14, 20, 0, 0, 0, time.UTC)
	client := NewAppleAccountClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":-41015,"retryAfter":"7200"}}`)),
		}, nil
	})})
	_, err := client.refresh(context.Background(), iCloudResourceChannelModel{
		Host: "appleid.apple.com", Cookie: "myacinfo=secret",
	}, now)
	var providerErr *appleAccountError
	if !errors.As(err, &providerErr) || providerErr.Category != "rate_limited" || providerErr.RetryAfter != 2*time.Hour {
		t.Fatalf("rate-limit response = %#v, want retry after 2h", err)
	}
}

func TestHMEGenerateAndReserveUseTheImportedRequestContext(t *testing.T) {
	const cookie = "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	paths := make([]string, 0, 2)
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.Method != http.MethodPost || request.URL.Query().Get("dsid") != "123" || request.Header.Get("Cookie") != cookie {
			t.Fatalf("unexpected mutation request: %s %s", request.Method, request.URL.String())
		}
		body := `{"success":true,"result":{"hme":"candidate@icloud.com"}}`
		switch request.URL.Path {
		case "/v1/hme/reserve":
			body = `{"success":true,"result":{"hme":{"hme":"candidate@icloud.com","anonymousId":"candidate-id","recipientMailId":"candidate-recipient","forwardToEmail":"icloud@aishop6.com","isActive":true}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	config := hmeConfig{Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie}
	candidate, updatedCookie, err := client.Generate(context.Background(), config)
	if err != nil || candidate != "candidate@icloud.com" || updatedCookie != cookie {
		t.Fatalf("generate: candidate=%q cookie=%q err=%v", candidate, updatedCookie, err)
	}
	alias, _, err := client.reserve(context.Background(), config, candidate, "ReMail", "")
	if err != nil || alias.Email != candidate || alias.AnonymousID != "candidate-id" || !alias.Active {
		t.Fatalf("reserve: alias=%#v err=%v", alias, err)
	}
	if len(paths) != 2 || paths[0] != "/v1/hme/generate" || paths[1] != "/v1/hme/reserve" {
		t.Fatalf("unexpected mutation sequence: %#v", paths)
	}
}
