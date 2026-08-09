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
