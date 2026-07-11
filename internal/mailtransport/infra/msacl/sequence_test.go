package msacl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/stretchr/testify/require"
)

type scriptedHTTPStep func(req *http.Request, followRedirects bool) (*http.Response, error)

type scriptedHTTPClient struct {
	t      *testing.T
	steps  []scriptedHTTPStep
	next   int
	follow bool
}

func (c *scriptedHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.t.Helper()
	require.Less(c.t, c.next, len(c.steps), "unexpected request: %s %s", req.Method, req.URL)
	step := c.steps[c.next]
	c.next++
	return step(req, c.follow)
}

func (c *scriptedHTTPClient) SetFollowRedirect(follow bool) {
	c.follow = follow
}

func (c *scriptedHTTPClient) GetFollowRedirect() bool {
	return c.follow
}

func (c *scriptedHTTPClient) requireDone() {
	c.t.Helper()
	require.Equal(c.t, len(c.steps), c.next)
}

func newScriptedSession(t *testing.T, steps ...scriptedHTTPStep) (*Session, *scriptedHTTPClient) {
	t.Helper()
	client := &scriptedHTTPClient{t: t, steps: steps, follow: true}
	return &Session{
		client:      client,
		ctx:         context.Background(),
		navHeaders:  map[string]string{"Accept": "text/html"},
		corsHeaders: map[string]string{"Accept": "application/json"},
		userAgent:   "sequence-test",
	}, client
}

func scriptedResponse(req *http.Request, statusCode int, finalURL, body string, headers map[string]string) *http.Response {
	responseRequest := new(http.Request)
	*responseRequest = *req
	if finalURL != "" {
		responseRequest.URL, _ = url.Parse(finalURL)
	}
	header := make(http.Header)
	for key, value := range headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    responseRequest,
	}
}

func requireRequest(t *testing.T, req *http.Request, method, rawURL string) {
	t.Helper()
	require.Equal(t, method, req.Method)
	require.Equal(t, rawURL, req.URL.String())
}

func TestAddSingleExplicitAliasUsesFreshCanaryAndConfirmsManagePage(t *testing.T) {
	const (
		prefix = "david123456"
		alias  = prefix + "@outlook.com"
	)
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/names/manage")
			require.True(t, follow)
			return scriptedResponse(req, 200, "https://account.live.com/names/manage", `<div>other123456@outlook.com</div>`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, addAssocIDURL)
			require.True(t, follow)
			page := `<form><input type="hidden" name="canary" value="fresh-canary"><input name="AddAssocIdOptions" value="LIVE"></form>`
			return scriptedResponse(req, 200, addAssocIDURL, page, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, addAssocIDURL)
			require.False(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "fresh-canary", fields.Get("canary"))
			require.Equal(t, prefix, fields.Get("AssociatedIdLive"))
			return scriptedResponse(req, 302, addAssocIDURL, "", map[string]string{
				"Location": "/names/manage?noteid=NOTE_AssociatedIdAddedWL",
			}), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/names/manage?noteid=NOTE_AssociatedIdAddedWL")
			require.True(t, follow)
			return scriptedResponse(req, 200, "https://account.live.com/names/manage?noteid=NOTE_AssociatedIdAddedWL", `<div>`+alias+`</div>`, nil), nil
		},
	)

	gotAlias, category, attempted, _, err := addSingleExplicitAlias(session, prefix, "owner@example.com", "", "")

	require.NoError(t, err)
	require.Equal(t, alias, gotAlias)
	require.Equal(t, aliasCategoryAdded, category)
	require.True(t, attempted)
	client.requireDone()
}

func TestAddSingleExplicitAliasReconcilesSameCandidateAfterLostResponse(t *testing.T) {
	const (
		prefix = "david123456"
		alias  = prefix + "@outlook.com"
	)
	firstSession, firstClient := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			return scriptedResponse(req, 200, "https://account.live.com/names/manage", `<div>other123456@outlook.com</div>`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			page := `<input type="hidden" name="canary" value="fresh-canary"><input name="AddAssocIdOptions" value="LIVE">`
			return scriptedResponse(req, 200, addAssocIDURL, page, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, addAssocIDURL)
			return nil, io.ErrUnexpectedEOF
		},
	)

	_, _, attempted, _, err := addSingleExplicitAlias(firstSession, prefix, "owner@example.com", "", "")
	require.Error(t, err)
	require.True(t, attempted)
	firstClient.requireDone()

	retrySession, retryClient := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/names/manage")
			return scriptedResponse(req, 200, "https://account.live.com/names/manage", `<div>`+alias+`</div>`, nil), nil
		},
	)
	gotAlias, category, attempted, _, err := addSingleExplicitAlias(retrySession, prefix, "owner@example.com", "", "")

	require.NoError(t, err)
	require.Equal(t, alias, gotAlias)
	require.Equal(t, aliasCategoryAdded, category)
	require.False(t, attempted)
	retryClient.requireDone()
}

func TestReconcileExplicitAliasesDoesNotSubmitAddAssocIDAgain(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/names/manage")
			return scriptedResponse(req, 200, "https://account.live.com/names/manage", `<div>first123456@outlook.com</div>`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/names/manage")
			return scriptedResponse(req, 200, "https://account.live.com/names/manage", `<div>first123456@outlook.com</div>`, nil), nil
		},
	)

	result, err := reconcileExplicitAliasesWithSession(session, []string{
		"first123456@outlook.com",
		"second123456@outlook.com",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"first123456@outlook.com"}, result.Aliases)
	require.Equal(t, []string{"second123456@outlook.com"}, result.Absent)
	require.Empty(t, result.Attempted)
	client.requireDone()
}

func TestGetExplicitAliasCredentialTypeKeepsOptionalProofsForLaterChallenge(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "login.live.com", req.URL.Hostname())
			require.Equal(t, "/GetCredentialType.srf", req.URL.Path)
			payload := map[string]any{
				"IfExistsResult": 0,
				"Credentials": map[string]any{
					"HasRemoteNGC": 0,
					"HasFido":      0,
					"OtcLoginEligibleProofs": []map[string]any{
						{"type": 1, "display": "r***@external.test", "data": "proof-email"},
						{"type": 3, "display": "+86 ******1234", "data": "proof-phone"},
						{"type": 10, "display": "Authenticator", "data": "proof-app"},
					},
				},
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			return scriptedResponse(req, 200, req.URL.String(), string(body), nil), nil
		},
	)

	proofs, err := getExplicitAliasCredentialType(
		session,
		"owner@outlook.com",
		"flow-token",
		"uaid-value",
		"opid-value",
		"https://login.live.com/login.srf",
	)

	require.NoError(t, err)
	require.Len(t, proofs, 3)
	require.Equal(t, []int{1, 3, 10}, []int{proofs[0].Type, proofs[1].Type, proofs[2].Type})
	client.requireDone()
}

func TestHandleProofsPageTriesSkipBeforeBinding(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/proofs/Add")
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "Skip", fields.Get("action"))
			require.Empty(t, fields.Get("EmailAddress"))
			return scriptedResponse(req, 200, "https://account.live.com/landing", `<html>done</html>`, nil), nil
		},
	)
	page := `<form action="/proofs/Add"><input type="hidden" name="canary" value="proof-canary"><input name="EmailAddress"></form>`

	nextPage, nextURL, mailbox, err := handleProofsPage(
		session,
		page,
		addAssocIDURL,
		"/proofs/Add",
		"",
		false,
		"owner@example.com",
		"",
	)

	require.NoError(t, err)
	require.Equal(t, `<html>done</html>`, nextPage)
	require.Equal(t, "https://account.live.com/landing", nextURL)
	require.Empty(t, mailbox)
	client.requireDone()
}

type emptyMailboxReader struct{}

func (emptyMailboxReader) List(context.Context, string, int, bool) ([]EmailObj, error) {
	return nil, nil
}

func (emptyMailboxReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return nil, nil
}

func TestHandleProofsPageResolvesRelativeActionBeforeBindingFallback(t *testing.T) {
	const page = `<form action="/proofs/Add"><input type="hidden" name="canary" value="proof-canary"><input name="EmailAddress"></form>`
	previousReader := activeMailboxReader()
	SetMailboxReader(emptyMailboxReader{})
	defer SetMailboxReader(previousReader)

	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "Skip", fields.Get("action"))
			return scriptedResponse(req, 200, "https://account.live.com/proofs/Add", page, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "AddProof", fields.Get("action"))
			require.Equal(t, "proof@aishop6.com", fields.Get("EmailAddress"))
			return nil, io.ErrUnexpectedEOF
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session.ctx = ctx

	_, _, _, err := handleProofsPage(
		session,
		page,
		addAssocIDURL,
		"/proofs/Add",
		"",
		false,
		"owner@example.com",
		"proof@aishop6.com",
	)

	require.Error(t, err)
	client.requireDone()
}

type sequencedMailboxReader struct {
	calls          atomic.Int32
	watcherStarted chan struct{}
	sendStarted    chan struct{}
	once           sync.Once
}

func (r *sequencedMailboxReader) List(ctx context.Context, mailbox string, _ int, _ bool) ([]EmailObj, error) {
	if r.calls.Add(1) == 1 {
		return []EmailObj{{ID: 1, To: mailbox, Subject: "Microsoft security code", Preview: "Security code 111111"}}, nil
	}
	r.once.Do(func() { close(r.watcherStarted) })
	select {
	case <-r.sendStarted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []EmailObj{{ID: 2, To: mailbox, Subject: "Microsoft security code", Preview: "Security code 654321"}}, nil
}

func (*sequencedMailboxReader) SearchByContent(context.Context, string, int) ([]EmailObj, error) {
	return nil, nil
}

func TestOTPSequenceStartsWatcherBeforeSendAndRefreshesCanary(t *testing.T) {
	previousReader := activeMailboxReader()
	reader := &sequencedMailboxReader{
		watcherStarted: make(chan struct{}),
		sendStarted:    make(chan struct{}),
	}
	SetMailboxReader(reader)
	defer SetMailboxReader(previousReader)

	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/API/Proofs/SendOtt")
			select {
			case <-reader.watcherStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "mail watcher did not start before SendOtt")
			}
			close(reader.sendStarted)
			require.Equal(t, "initial-canary", req.Header.Get("canary"))
			return scriptedResponse(req, 200, req.URL.String(), `{"apiCanary":"refreshed-canary"}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/API/Proofs/VerifyCode")
			require.Equal(t, "refreshed-canary", req.Header.Get("canary"))
			var payload map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
			require.Equal(t, "654321", payload["code"])
			return scriptedResponse(req, 200, req.URL.String(), `{"route":"verified"}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/return?route=verified")
			return scriptedResponse(req, 200, req.URL.String(), `<html>verified</html>`, nil), nil
		},
	)
	proofs, err := json.Marshal(`[{"type":"Email","name":"pr***@aishop6.com","epid":"proof-id"}]`)
	require.NoError(t, err)
	page := fmt.Sprintf(
		`{"apiCanary":"initial-canary","token":"proof-token","proofPurpose":"UnfamiliarLocationHard","rawProofList":%s,"return":{"url":"https://account.live.com/return"}}`,
		proofs,
	)

	nextPage, nextURL, mailbox, err := handleOTPVerification(
		session,
		page,
		"https://account.live.com/identity/confirm",
		"owner@example.com",
		"",
		nil,
		"proof@aishop6.com",
	)

	require.NoError(t, err)
	require.Equal(t, `<html>verified</html>`, nextPage)
	require.Equal(t, "https://account.live.com/return?route=verified", nextURL)
	require.Equal(t, "proof@aishop6.com", mailbox)
	client.requireDone()
}

func TestOTPVerifyLostResponseRemainsRetryable(t *testing.T) {
	previousReader := activeMailboxReader()
	reader := &sequencedMailboxReader{
		watcherStarted: make(chan struct{}),
		sendStarted:    make(chan struct{}),
	}
	SetMailboxReader(reader)
	defer SetMailboxReader(previousReader)

	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			select {
			case <-reader.watcherStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "mail watcher did not start before SendOtt")
			}
			close(reader.sendStarted)
			return scriptedResponse(req, 200, req.URL.String(), `{"apiCanary":"refreshed-canary"}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			return scriptedResponse(req, 503, req.URL.String(), `{"error":{"code":"temporary"}}`, nil), nil
		},
	)
	proofs, err := json.Marshal(`[{"type":"Email","name":"pr***@aishop6.com","epid":"proof-id"}]`)
	require.NoError(t, err)
	page := fmt.Sprintf(
		`{"apiCanary":"initial-canary","token":"proof-token","proofPurpose":"UnfamiliarLocationHard","rawProofList":%s,"return":{"url":"https://account.live.com/return"}}`,
		proofs,
	)

	_, _, _, err = handleOTPVerification(
		session,
		page,
		"https://account.live.com/identity/confirm",
		"owner@example.com",
		"",
		nil,
		"proof@aishop6.com",
	)

	require.Error(t, err)
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, AuthStatusRequestError, authErr.Status)
	client.requireDone()
}

func TestExplicitAliasPasswordCheckTreatsServerFailureAsRetryable(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://login.live.com/checkpassword.srf")
			return scriptedResponse(req, 503, req.URL.String(), `{"validationresult":"failed"}`, nil), nil
		},
	)

	_, err := checkExplicitAliasPassword(
		session,
		"owner@example.com",
		"secret",
		"uaid",
		"https://login.live.com/login.srf",
	)

	require.Error(t, err)
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, AuthStatusRequestError, authErr.Status)
	client.requireDone()
}

func TestDevicePasswordCheckTreatsServerFailureAsRetryable(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://login.live.com/checkpassword.srf")
			return scriptedResponse(req, 503, req.URL.String(), `{"validationresult":"failed"}`, nil), nil
		},
	)

	_, err := checkPassword(session, "owner@example.com", "secret", "uaid", 1)

	require.Error(t, err)
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, AuthStatusRequestError, authErr.Status)
	client.requireDone()
}

func TestDeclineKMSIUsesCurrentPromptAsReferer(t *testing.T) {
	const kmsiURL = "https://login.live.com/ppsecure/post.srf?kmsi=1"
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			require.Equal(t, kmsiURL, req.Header.Get("Referer"))
			return scriptedResponse(req, 200, "https://account.live.com/auth/redirect", `<html>continued</html>`, nil), nil
		},
	)
	page := `{"sPageId":"i5245","sFT":"kmsi-ppft","urlPost":"https://login.live.com/ppsecure/post.srf"}`

	nextPage, nextURL, _, err := declineKMSI(session, page, kmsiURL, kmsiURL)

	require.NoError(t, err)
	require.Equal(t, `<html>continued</html>`, nextPage)
	require.Equal(t, "https://account.live.com/auth/redirect", nextURL)
	client.requireDone()
}

func TestOneTimeFormPostIsNotReplayedAfterLostResponse(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/identity/continue")
			return nil, io.ErrUnexpectedEOF
		},
	)

	_, err := postEmptyForm(
		session,
		"https://account.live.com/identity/continue",
		"https://account.live.com/identity/confirm",
	)

	require.Error(t, err)
	client.requireDone()
}
