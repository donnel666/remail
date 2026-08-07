package icloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
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
			Body: io.NopCloser(strings.NewReader(`{"success":true,"result":{"selectedForwardTo":"target@gmail.com","hmeEmails":[{"hme":"abc@icloud.com","anonymousId":"anon-1","label":"label","note":"note","forwardToEmail":"target@gmail.com","domain":"icloud.com","recipientMailId":"mail-1","isActive":true,"createTimestamp":1700000000000}]}}`)),
		}, nil
	})})
	result, err := client.List(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if seen == nil || seen.URL.Path != "/v2/hme/list" || seen.URL.Query().Get("dsid") != "123" || seen.Header.Get("Cookie") != cookie {
		t.Fatalf("unexpected HME request: %#v", seen)
	}
	if len(result.Aliases) != 1 || result.Aliases[0].Email != "abc@icloud.com" || result.SelectedForwardTo != "target@gmail.com" {
		t.Fatalf("unexpected HME result: %#v", result)
	}
	if !strings.Contains(result.UpdatedCookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
		t.Fatalf("Set-Cookie was not merged: %q", result.UpdatedCookie)
	}
}

func TestHMEListDoesNotExposeSessionMaterialOnUnauthorized(t *testing.T) {
	const secret = "session-secret-value"
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(secret))}, nil
	})})
	_, err := client.List(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering",
		Cookie: "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token",
	})
	if err == nil {
		t.Fatal("List() error = nil, want unauthorized error")
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
	_, err := client.List(context.Background(), hmeConfig{
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
	_, err := client.List(context.Background(), hmeConfig{
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
	_, err := client.List(context.Background(), hmeConfig{
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
	_, err := client.List(context.Background(), hmeConfig{
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
			`{"success":true,"result":{"selectedForwardTo":"target@gmail.com"}}`,
		))}, nil
	})})
	_, err := client.List(context.Background(), hmeConfig{
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
		body := `{"success":true,"result":{"selectedForwardTo":"target@gmail.com","total":2,"hasMore":true,"nextPageToken":"page-2","hmeEmails":[{"hme":"one@icloud.com","anonymousId":"one","isActive":true}]}}`
		if request.URL.Query().Get("nextPageToken") == "page-2" {
			body = `{"success":true,"result":{"total":2,"hasMore":false,"hmeEmails":[{"hme":"two@icloud.com","anonymousId":"two","isActive":true}]}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	result, err := client.List(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	if err != nil || !result.Complete || len(result.Aliases) != 2 || calls != 2 {
		t.Fatalf("paged list: result=%#v calls=%d err=%v", result, calls, err)
	}
	if result.Aliases[1].ForwardToEmail != "target@gmail.com" {
		t.Fatalf("account forwarding target was not inherited: %#v", result.Aliases[1])
	}

	incomplete := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			`{"success":true,"result":{"total":2,"hasMore":false,"hmeEmails":[{"hme":"one@icloud.com","anonymousId":"one","isActive":true}]}}`,
		))}, nil
	})})
	_, err = incomplete.List(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	providerErr, ok := err.(*hmeError)
	if !ok || providerErr.Category != "snapshot_incomplete" {
		t.Fatalf("incomplete total must be rejected, err=%#v", err)
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
	_, err := client.List(context.Background(), hmeConfig{
		Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie,
	})
	providerErr, ok := err.(*hmeError)
	if !ok || !strings.Contains(providerErr.UpdatedCookie, "X-APPLE-WEBAUTH-TOKEN=rotated") {
		t.Fatalf("earlier page cookie must survive a later transport error: calls=%d err=%#v", calls, err)
	}
}

func TestHMEGenerateReserveAndActivateUseTheImportedRequestContext(t *testing.T) {
	const cookie = "X-APPLE-DS-WEB-SESSION-TOKEN=session; X-APPLE-WEBAUTH-USER=user; X-APPLE-WEBAUTH-TOKEN=token"
	paths := make([]string, 0, 3)
	client := NewHMEClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.Method != http.MethodPost || request.URL.Query().Get("dsid") != "123" || request.Header.Get("Cookie") != cookie {
			t.Fatalf("unexpected mutation request: %s %s", request.Method, request.URL.String())
		}
		body := `{"success":true,"result":{"hme":"candidate@icloud.com"}}`
		if request.URL.Path == "/v1/hme/reserve" {
			body = `{"success":true,"result":{"hme":{"hme":"candidate@icloud.com","anonymousId":"candidate-id","forwardToEmail":"target@gmail.com","isActive":true}}}`
		} else if request.URL.Path == "/v1/hme/activate" {
			body = `{"success":true}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	config := hmeConfig{Host: "p119-maildomainws.icloud.com", DSID: "123", ClientID: "client", ClientBuildNumber: "build", ClientMasteringNumber: "mastering", Cookie: cookie}
	candidate, updatedCookie, err := client.Generate(context.Background(), config)
	if err != nil || candidate != "candidate@icloud.com" || updatedCookie != cookie {
		t.Fatalf("generate: candidate=%q cookie=%q err=%v", candidate, updatedCookie, err)
	}
	alias, _, err := client.Reserve(context.Background(), config, candidate, "ReMail", "")
	if err != nil || alias.Email != candidate || alias.AnonymousID != "candidate-id" {
		t.Fatalf("reserve: alias=%#v err=%v", alias, err)
	}
	if _, err := client.Activate(context.Background(), config, alias.AnonymousID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if len(paths) != 3 || paths[0] != "/v1/hme/generate" || paths[1] != "/v1/hme/reserve" || paths[2] != "/v1/hme/activate" {
		t.Fatalf("unexpected mutation sequence: %#v", paths)
	}
}
