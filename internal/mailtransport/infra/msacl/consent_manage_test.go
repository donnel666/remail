package msacl

import (
	"io"
	"net/url"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/stretchr/testify/require"
)

func TestParseMicrosoftConsentClientIDsDeduplicatesSafeEntries(t *testing.T) {
	page := `<a href="/consent/Edit?client_id=0000000040C8F39E">one</a>
<a href="https://account.live.com/consent/Edit?client_id=0000000040c8f39e&amp;mkt=zh-CN">duplicate</a>
<a href="/consent/Edit?client_id=f6061517-4417-4749-a5b6-5bba57f9e6cc">two</a>
<a href="/consent/Edit?client_id=bad%2Fvalue">bad</a>`

	require.Equal(t, []string{
		"0000000040C8F39E",
		"f6061517-4417-4749-a5b6-5bba57f9e6cc",
	}, parseMicrosoftConsentClientIDs(page))
}

func TestMicrosoftConsentManageRecognizesLocalizedEmptyState(t *testing.T) {
	page := `<script>var ServerData={"sPageId":"i6148"};</script><h1 id="iPageTitle">Applications et services autorisés</h1><p class="modulerow">Aucune application.</p>`
	require.True(t, isMicrosoftConsentManagePage(page))
	require.False(t, isMicrosoftConsentManagePage(`<script>var ServerData={"sPageId":"i6148"};</script>`))
}

func TestHandleAccountPagesCompletesCredentialActionEmailEnrollment(t *testing.T) {
	const (
		currentURL  = "https://account.live.com/interrupt/credentialaction"
		enrollURL   = "https://account.live.com/api/v1.0/auth/methods/email"
		activateURL = "https://account.live.com/api/v1.0/auth/methods/email/method-id/activate"
		continueURL = "https://login.live.com/ppsecure/post.srf"
		returnURL   = "https://login.live.com/oauth20_remoteconnect.srf"
	)
	page := `<script>var ServerData={"sPageId":"Account_CredentialActionInterruptPage_Client","hpgid":123,"hpgact":0,"acmaInitialConfig":{"canary":"api-canary","navigation":{"baseRouteUri":"https://account.live.com"},"request":{"correlationId":"correlation-id","sessionId":"session-id"}},"acmaInitialResponse":{"continuationToken":"initial-token","state":"interactionRequired","action":"enroll","infoType":"email","_embedded":{"methods":[{"id":"","type":"email","hint":"","_links":{"enroll":{"href":"/api/v1.0/auth/methods/email"}}}],"user":[{"displayName":"owner"}]}}};</script>`
	finishedPage := `<html>finished</html>`
	previousReader := activeMailboxReader()
	reader := &sequencedMailboxReader{watcherStarted: make(chan struct{}), sendStarted: make(chan struct{})}
	SetMailboxReader(reader)
	defer SetMailboxReader(previousReader)
	previousDomains := activeAuxiliaryDomains()
	SetAuxiliaryDomains([]string{"recovery.test"})
	defer SetAuxiliaryDomains(previousDomains)

	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, enrollURL)
			require.True(t, follow)
			select {
			case <-reader.watcherStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "mail watcher did not start before credential enrollment")
			}
			close(reader.sendStarted)
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "proof@recovery.test", payload["email"])
			require.Equal(t, "initial-token", payload["continuationToken"])
			require.Equal(t, "api-canary", req.Header.Get("canary"))
			return scriptedResponse(req, 200, enrollURL, `{"continuationToken":"activate-token","state":"interactionRequired","action":"activate","id":"method-id","type":"email","hint":"proof@recovery.test","_links":{"activate":{"href":"/api/v1.0/auth/methods/email/method-id/activate"}}}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, activateURL)
			require.True(t, follow)
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "654321", payload["otp"])
			details := asMap(payload["activationDetails"])
			require.Equal(t, "method-id", details["id"])
			require.Equal(t, "proof@recovery.test", details["displayName"])
			return scriptedResponse(req, 200, activateURL, `{"continuationToken":"continue-token","state":"continue","action":"externalRedirect","clientHints":["slt=slt-value"],"_links":{"continue":{"href":"https://login.live.com/ppsecure/post.srf","params":{"ipt":"relay-token"}}}}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, continueURL)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "relay-token", fields.Get("ipt"))
			require.Equal(t, "continue-token", fields.Get("continuationToken"))
			require.Equal(t, "slt-value", fields.Get("slt"))
			return scriptedResponse(req, 200, returnURL, finishedPage, nil), nil
		},
	)

	gotPage, gotURL, mailbox, err := handleAccountPagesWithOptions(session, page, currentURL, "", 10, "owner@outlook.com", nil, false, "proof@recovery.test")

	require.NoError(t, err)
	require.Equal(t, finishedPage, gotPage)
	require.Equal(t, returnURL, gotURL)
	require.Equal(t, "proof@recovery.test", mailbox)
	client.requireDone()
}

func TestHandleAccountPagesSubmitsProofRelayWithoutSkipFields(t *testing.T) {
	const (
		action    = "https://account.live.com/proofs/Verify"
		returnURL = "https://account.live.com/consent/Manage"
	)
	page := `<form id="fmHF" action="` + action + `" method="post"><input type="hidden" name="ipt" value="proof-token"><input type="hidden" name="pprid" value="proof-request"><input type="hidden" name="uaid" value="uaid-value"></form><script>DoSubmit()</script>`
	finishedPage := `<html>finished</html>`
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, action)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "proof-token", fields.Get("ipt"))
			require.Empty(t, fields.Get("action"))
			require.Empty(t, fields.Get("iProofOptions"))
			return scriptedResponse(req, 200, returnURL, finishedPage, nil), nil
		},
	)

	gotPage, gotURL, _, err := handleAccountPagesWithOptions(session, page, "https://login.live.com/ppsecure/post.srf", "", 10, "owner@outlook.com", nil, true, "")

	require.NoError(t, err)
	require.Equal(t, finishedPage, gotPage)
	require.Equal(t, returnURL, gotURL)
	client.requireDone()
}

func TestHandleProofsPageSendsAndVerifiesExistingEmailProof(t *testing.T) {
	const (
		action    = "https://account.live.com/proofs/Verify"
		returnURL = "https://login.live.com/oauth20_remoteconnect.srf"
		proof     = "OTT||proof@recovery.test||Email||0||a"
	)
	page := `<script>var $Config={"apiCanary":"api-canary","eipt":"eipt-value","correlationId":"correlation-id","uaid":"uaid-value","uiflvr":1001,"scid":100146,"hpgid":201028};</script>
<script>var ServerData = {sContext:'CatB',sAction:'Compliance',sNetId:'net\x2did',sProofData:'destination\x2dtoken\x7cnull'}; SendOtt</script>
<form id="frmVerifyProof" method="post" action="` + action + `"><input type="radio" name="proof" value="` + proof + `"><input type="hidden" name="iProofOptions" value=""><input type="hidden" name="canary" value="form-canary"><input type="hidden" name="action" value="VerifyProof"><input name="iOttText"></form>`
	previousReader := activeMailboxReader()
	reader := &sequencedMailboxReader{watcherStarted: make(chan struct{}), sendStarted: make(chan struct{})}
	SetMailboxReader(reader)
	defer SetMailboxReader(previousReader)
	previousDomains := activeAuxiliaryDomains()
	SetAuxiliaryDomains([]string{"recovery.test"})
	defer SetAuxiliaryDomains(previousDomains)

	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/API/Proofs/SendOtt")
			require.True(t, follow)
			select {
			case <-reader.watcherStarted:
			case <-time.After(time.Second):
				require.FailNow(t, "mail watcher did not start before SendOtt")
			}
			close(reader.sendStarted)
			payload := decodeJSONRequest(t, req)
			require.Equal(t, "destination-token", payload["destination"])
			require.Equal(t, "Compliance", payload["action"])
			require.Equal(t, "net-id", payload["netid"])
			require.Equal(t, "CatB", payload["cxt"])
			require.Equal(t, 1001, asInt(payload["uiflvr"]))
			require.Equal(t, 100146, asInt(payload["scid"]))
			require.Equal(t, 201028, asInt(payload["hpgid"]))
			require.Equal(t, "uaid-value", payload["uaid"])
			require.Equal(t, "api-canary", req.Header.Get("canary"))
			require.Equal(t, "eipt-value", req.Header.Get("eipt"))
			require.Equal(t, "2", req.Header.Get("x-ms-apiVersion"))
			require.Equal(t, "xhr", req.Header.Get("x-ms-apiTransport"))
			require.Equal(t, "correlation-id", req.Header.Get("x-ms-correlation-id"))
			return scriptedResponse(req, 200, req.URL.String(), `{}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, action)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, proof, fields.Get("iProofOptions"))
			require.Equal(t, "654321", fields.Get("iOttText"))
			require.Equal(t, "0", fields.Get("GeneralVerify"))
			return scriptedResponse(req, 200, returnURL, `<html>finished</html>`, nil), nil
		},
	)

	gotPage, gotURL, mailbox, err := handleProofsPage(session, page, action, action, "", false, "owner@outlook.com", "pending@recovery.test")

	require.NoError(t, err)
	require.Equal(t, `<html>finished</html>`, gotPage)
	require.Equal(t, returnURL, gotURL)
	require.Equal(t, "proof@recovery.test", mailbox)
	client.requireDone()
}

func TestProofsAddActionIsBindingPageWithoutRenderedInputs(t *testing.T) {
	require.True(t, isAddEmailPage(`<html><div id="app"></div></html>`, "https://account.live.com/proofs/Add?mkt=ZH-CN"))
	require.False(t, isAddEmailPage(`<html><script>AddProof EmailAddress</script></html>`, "https://account.live.com/proofs/Verify"))
}

func TestLoadMicrosoftProofsAddPageSubmitsAuthenticatedRelay(t *testing.T) {
	const relayAction = microsoftProofsAddURL + "?mkt=es-ES"
	relayPage := `<form action="` + relayAction + `"><input type="hidden" name="ipt" value="proof-token"><input type="hidden" name="pprid" value="proof-request"><input type="hidden" name="uaid" value="uaid-value"></form><script>DoSubmit()</script>`
	addPage := `<form action="/proofs/Add"><input type="hidden" name="canary" value="proof-canary"><input name="EmailAddress"></form>`
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftProofsAddURL)
			require.True(t, follow)
			return scriptedResponse(req, 200, "https://login.live.com/login.srf", relayPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, relayAction)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "proof-token", fields.Get("ipt"))
			require.Equal(t, "proof-request", fields.Get("pprid"))
			return scriptedResponse(req, 200, microsoftProofsAddURL, addPage, nil), nil
		},
	)

	page, currentURL, err := loadMicrosoftProofsAddPage(session)

	require.NoError(t, err)
	require.Equal(t, addPage, page)
	require.Equal(t, microsoftProofsAddURL, currentURL)
	client.requireDone()
}

func TestLoadMicrosoftProofsAddPageWithPasswordUsesAliasContext(t *testing.T) {
	const (
		email       = "owner@outlook.com"
		postURL     = "https://login.live.com/ppsecure/post.srf?opid=opid-value&uaid=uaid-value"
		relayAction = microsoftProofsAddURL + "?mkt=ja-JP"
	)
	loginPage := `<script>var ServerData={"sFT":"login-ppft","urlPost":"` + postURL + `"};</script>`
	credentialPage := `<script>var ServerData={"sFT":"credential-ppft","urlPost":"` + postURL + `"};</script>`
	relayPage := `<form action="` + relayAction + `"><input type="hidden" name="ipt" value="proof-token"><input type="hidden" name="pprid" value="proof-request"><input type="hidden" name="uaid" value="uaid-value"></form><script>DoSubmit()</script>`
	addPage := `<form action="/proofs/Add"><input type="hidden" name="canary" value="proof-canary"><input name="EmailAddress"></form>`
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, addAssocIDURL)
			require.True(t, follow)
			return scriptedResponse(req, 200, "https://login.live.com/login.srf", loginPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, postURL)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, email, fields.Get("login"))
			require.Equal(t, "login-ppft", fields.Get("PPFT"))
			return scriptedResponse(req, 200, "https://login.live.com/login.srf", credentialPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://login.live.com/checkpassword.srf")
			require.True(t, follow)
			return scriptedResponse(req, 200, req.URL.String(), `{"validationresult":"succeed","vanguardflowtoken":"vft"}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, postURL)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, email, fields.Get("login"))
			require.Equal(t, "credential-ppft", fields.Get("PPFT"))
			require.Equal(t, "vft", fields.Get("vanguardflowtoken"))
			return scriptedResponse(req, 200, postURL, relayPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, relayAction)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "proof-token", fields.Get("ipt"))
			require.Equal(t, "proof-request", fields.Get("pprid"))
			return scriptedResponse(req, 200, microsoftProofsAddURL, addPage, nil), nil
		},
	)

	page, currentURL, err := loadMicrosoftProofsAddPageWithPassword(session, email, "password")

	require.NoError(t, err)
	require.Equal(t, addPage, page)
	require.Equal(t, microsoftProofsAddURL, currentURL)
	client.requireDone()
}

func TestLoginMicrosoftConsentManageUsesWSFedSession(t *testing.T) {
	const (
		email   = "owner@outlook.com"
		postURL = "https://login.live.com/ppsecure/post.srf?opid=opid-value&uaid=uaid-value"
	)
	loginPage := `<script>var ServerData={"sFT":"login-ppft","urlPost":"` + postURL + `"};</script>`
	kmsiPage := `<script>var ServerData={"sFT":"kmsi-ppft","urlPost":"https://login.live.com/ppsecure/post.srf?opid=kmsi"};</script><div>LoginOptions type</div>`
	relayPage := `<form method="post" action="https://account.live.com/auth/redirect"><input type="hidden" name="code" value="relay-code"></form>`

	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftConsentLoginURL)
			require.True(t, follow)
			return scriptedResponse(req, 200, microsoftConsentLoginURL, loginPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://fpt.live.com/?session_id=uaid-value&CustomerId="+microsoftConsentFPTCustomerID+"&PageId=SI")
			require.True(t, follow)
			return scriptedResponse(req, 200, req.URL.String(), "", nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://fpt.live.com/Images/Clear.PNG?ctx=jscb1.0&session_id=uaid-value&CustomerId="+microsoftConsentFPTCustomerID)
			require.True(t, follow)
			return scriptedResponse(req, 200, req.URL.String(), "", nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://login.live.com/GetCredentialType.srf?opid=opid-value&id=38936&mkt=ZH-CN&lc=2052&uaid=uaid-value")
			require.True(t, follow)
			return scriptedResponse(req, 200, req.URL.String(), `{"IfExistsResult":0}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://login.live.com/checkpassword.srf")
			require.True(t, follow)
			return scriptedResponse(req, 200, req.URL.String(), `{"validationresult":"succeed","vanguardflowtoken":"vft"}`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, postURL)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, email, fields.Get("login"))
			require.Equal(t, "login-ppft", fields.Get("PPFT"))
			require.Equal(t, "Passport", fields.Get("PPSX"))
			return scriptedResponse(req, 200, "https://login.live.com/login.srf", kmsiPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://login.live.com/ppsecure/post.srf?opid=kmsi")
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "kmsi-ppft", fields.Get("PPFT"))
			require.Equal(t, "28", fields.Get("type"))
			return scriptedResponse(req, 200, "https://login.live.com/ppsecure/post.srf", relayPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/auth/redirect")
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "relay-code", fields.Get("code"))
			return scriptedResponse(req, 200, microsoftConsentManageURL, `<script>consentManageService</script>`, nil), nil
		},
	)

	page, finalURL, err := loginMicrosoftConsentManage(session, email, "password", "", "")

	require.NoError(t, err)
	require.Equal(t, microsoftConsentManageURL, finalURL)
	require.True(t, isMicrosoftConsentManagePage(page))
	client.requireDone()
}

func TestLoginMicrosoftConsentManageReusesAuthenticatedRelay(t *testing.T) {
	privacyPage := `<form method="post" action="https://privacynotice.account.microsoft.com/notice"><input type="hidden" name="ru" value="login-relay"></form>`
	privacyRedirectPage := `<script>var redirectUrl = 'https://login.live.com/login.srf?id=38936\u0026opid=relay'; window.location.replace(encodeURI(redirectUrl));</script>`
	relayPage := `<form method="post" action="https://account.live.com/auth/redirect"><input type="hidden" name="code" value="relay-code"></form>`
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftConsentLoginURL)
			require.True(t, follow)
			return scriptedResponse(req, 200, microsoftConsentLoginURL, privacyPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://privacynotice.account.microsoft.com/notice")
			require.True(t, follow)
			return scriptedResponse(req, 200, "https://privacynotice.account.microsoft.com/notice", privacyRedirectPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://login.live.com/login.srf?id=38936&opid=relay")
			require.True(t, follow)
			return scriptedResponse(req, 200, req.URL.String(), relayPage, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/auth/redirect")
			require.True(t, follow)
			return scriptedResponse(req, 200, microsoftConsentManageURL, `<script>consentManageService</script>`, nil), nil
		},
	)

	_, finalURL, err := loginMicrosoftConsentManage(session, "owner@outlook.com", "password", "", "")

	require.NoError(t, err)
	require.Equal(t, microsoftConsentManageURL, finalURL)
	client.requireDone()
}

func TestRemoveAllMicrosoftConsentsUsesOneSessionAndVerifiesEmpty(t *testing.T) {
	const clientID = "0000000040C8F39E"
	manageWithApp := `<a href="/consent/Edit?client_id=` + clientID + `">Graph app</a>`
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/consent/Edit?client_id="+clientID)
			require.True(t, follow)
			page := `<form method="post" action="/consent/Edit?client_id=` + clientID + `"><input type="hidden" name="canary" value="fresh-canary"></form>`
			return scriptedResponse(req, 200, req.URL.String(), page, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/consent/Edit?client_id="+clientID)
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "fresh-canary", fields.Get("canary"))
			return scriptedResponse(req, 200, microsoftConsentManageURL, `<html>removed</html>`, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftConsentManageURL)
			return scriptedResponse(req, 200, microsoftConsentManageURL, `<script>consentManageService</script>`, nil), nil
		},
	)

	result, err := removeAllMicrosoftConsents(session, manageWithApp)

	require.NoError(t, err)
	require.Equal(t, ConsentCleanupResult{Before: 1, Removed: 1, Remaining: 0}, result)
	client.requireDone()
}

func TestListMicrosoftConsentClientIDsRejectsUnrecognizedEmptyPage(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, microsoftConsentManageURL)
			return scriptedResponse(req, 200, microsoftConsentManageURL, `<html>temporary error</html>`, nil), nil
		},
	)

	_, err := listMicrosoftConsentClientIDs(session)

	require.Error(t, err)
	client.requireDone()
}

func TestListMicrosoftConsentClientIDsDoesNotClassifyUpstream429AsProxyFailure(t *testing.T) {
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, http.StatusTooManyRequests, microsoftConsentManageURL, `<html>rate limited</html>`, nil), nil
		},
	)
	session.usesProxy = true
	session.retryCredentialTypeRateLimits = true

	_, err := listMicrosoftConsentClientIDs(session)
	result := mapAuthError(err)

	require.Error(t, err)
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, 60*time.Second, authErr.RetryAfter)
	require.Equal(t, "rate_limited", result.Category)
	require.False(t, result.ProxyFailure)
	client.requireDone()
}

func TestRemoveAllMicrosoftConsentsFailsClosedWhenEntryRemains(t *testing.T) {
	const clientID = "0000000040C8F39E"
	manageWithApp := `<a href="/consent/Edit?client_id=` + clientID + `">Graph app</a>`
	session, client := newScriptedSession(t,
		func(req *http.Request, _ bool) (*http.Response, error) {
			page := `<form action="/consent/Edit?client_id=` + clientID + `"><input type="hidden" name="canary" value="fresh-canary"></form>`
			return scriptedResponse(req, 200, req.URL.String(), page, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, microsoftConsentManageURL, manageWithApp, nil), nil
		},
		func(req *http.Request, _ bool) (*http.Response, error) {
			return scriptedResponse(req, 200, microsoftConsentManageURL, manageWithApp, nil), nil
		},
	)

	result, err := removeAllMicrosoftConsents(session, manageWithApp)

	require.Error(t, err)
	require.Equal(t, ConsentCleanupResult{Before: 1, Removed: 0, Remaining: 1}, result)
	client.requireDone()
}

func TestAddExplicitAliasCandidatesWithSessionDoesNotCreateAnotherSession(t *testing.T) {
	const alias = "david123456@outlook.com"
	session, client := newScriptedSession(t,
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, addAssocIDURL)
			require.True(t, follow)
			page := `<form method="post" action="` + addAssocIDURL + `"><input type="hidden" name="relay" value="1"></form>`
			return scriptedResponse(req, 200, "https://login.live.com/login.srf?wreply="+url.QueryEscape(addAssocIDURL), page, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, addAssocIDURL)
			require.True(t, follow)
			return scriptedResponse(req, 200, addAssocIDURL, `<input name="canary" value="login-canary">`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/names/manage")
			require.True(t, follow)
			page := `<form method="post" action="https://account.live.com/names/manage"><input type="hidden" name="relay" value="1"></form>`
			return scriptedResponse(req, 200, "https://login.live.com/login.srf?wreply="+url.QueryEscape("https://account.live.com/names/manage"), page, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, "https://account.live.com/names/manage")
			require.True(t, follow)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			fields, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, "1", fields.Get("relay"))
			return scriptedResponse(req, 200, addAssocIDURL, `<input name="canary" value="relay-canary">`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, "https://account.live.com/names/manage")
			require.True(t, follow)
			return scriptedResponse(req, 200, "https://account.live.com/names/manage", `<html>managed</html>`, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodGet, addAssocIDURL)
			require.True(t, follow)
			page := `<input name="canary" value="add-canary"><input name="AddAssocIdOptions" value="LIVE">`
			return scriptedResponse(req, 200, addAssocIDURL, page, nil), nil
		},
		func(req *http.Request, follow bool) (*http.Response, error) {
			requireRequest(t, req, http.MethodPost, addAssocIDURL)
			require.False(t, follow)
			return scriptedResponse(req, 302, addAssocIDURL, "", map[string]string{
				"Location": "/names/manage?noteid=NOTE_AssociatedIdAddedWL",
			}), nil
		},
	)

	results := addExplicitAliasCandidatesWithSession(session, "owner@outlook.com", "", "owner-recovery@example.com", []string{alias})

	require.Len(t, results, 1)
	require.Equal(t, []string{alias}, results[0].Aliases)
	client.requireDone()
}
