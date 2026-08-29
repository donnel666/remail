package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func configureNodeLocTest(t *testing.T, minimumTrustLevel int) string {
	t.Helper()
	before := runtimeconfig.Snapshot()
	t.Cleanup(func() {
		settings := make([]settingsdomain.Setting, 0, len(before))
		for key, value := range before {
			settings = append(settings, settingsdomain.Setting{Key: key, Value: value})
		}
		runtimeconfig.Replace(settings)
	})
	runtimeconfig.SetMany([]settingsdomain.Setting{
		{Key: "register_enabled", Value: "true"},
		{Key: "registration_email_whitelist", Value: "qq.com"},
		{Key: "registration_reward_amount", Value: "0"},
		{Key: "nodeloc_oauth_enabled", Value: "true"},
		{Key: "nodeloc_client_id", Value: "client-id"},
		{Key: "nodeloc_client_secret", Value: "client-secret"},
		{Key: "nodeloc_callback_url", Value: defaultNodeLocCallback},
		{Key: "nodeloc_minimum_trust_level", Value: strconv.Itoa(minimumTrustLevel)},
	})
	return defaultNodeLocCallback
}

func newNodeLocTestProvider(t *testing.T, callbackURL string, trustLevel int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	tokenCalls := &atomic.Int32{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			if r.Method != http.MethodPost || r.ParseForm() != nil ||
				r.Form.Get("client_id") != "client-id" ||
				r.Form.Get("client_secret") != "client-secret" ||
				r.Form.Get("code") != "authorization-code" ||
				r.Form.Get("redirect_uri") != callbackURL ||
				r.Form.Get("grant_type") != "authorization_code" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-token"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":42,"username":"node-user","name":"Node User","email":"member@outside.test","trust_level":` + strconv.Itoa(trustLevel) + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	return provider, tokenCalls
}

func nodeLocTestClient(provider *httptest.Server) *nodeLocClient {
	return &nodeLocClient{httpClient: provider.Client(), tokenURL: provider.URL + "/token", userURL: provider.URL + "/userinfo"}
}

func nodeLocFlow(t *testing.T, response *httptest.ResponseRecorder) (string, *http.Cookie) {
	t.Helper()
	authorizeURL, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	state := authorizeURL.Query().Get("state")
	require.NotEmpty(t, state)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == nodeLocStateCookieName {
			return state, cookie
		}
	}
	require.FailNow(t, "nodeloc state cookie was not set")
	return "", nil
}

func nodeLocPendingCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == nodeLocPendingCookieName && cookie.Value != "" {
			return cookie
		}
	}
	require.FailNow(t, "nodeloc pending cookie was not set")
	return nil
}

func TestNodeLocClientExchange(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") != "code-1" || r.FormValue("redirect_uri") != defaultNodeLocCallback {
			t.Fatalf("unexpected token request: method=%s form=%v", r.Method, r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token-1"}`))
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"username":"node-user","name":"Node User","email":"node@example.com","trust_level":2}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := newNodeLocClient()
	client.tokenURL = server.URL + "/token"
	client.userURL = server.URL + "/userinfo"
	profile, err := client.Exchange(context.Background(), "code-1", nodeLocSettings{
		ClientID: "client", ClientSecret: "secret", CallbackURL: defaultNodeLocCallback,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "42" || profile.Username != "node-user" || profile.Email != "node@example.com" || profile.TrustLevel != 2 {
		t.Fatalf("unexpected profile: %+v", profile)
	}
}

func TestNodeLocOAuthCallbackRequiresVerifiedAccountOwnership(t *testing.T) {
	callbackURL := configureNodeLocTest(t, 2)
	provider, _ := newNodeLocTestProvider(t, callbackURL, 2)
	h := newTestHandler()
	h.module.nodeLocClient = nodeLocTestClient(provider)
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/nodeloc", nil))
	require.Equal(t, http.StatusFound, start.Code)
	authorizeURL, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, nodeLocAuthorizeEndpoint, authorizeURL.Scheme+"://"+authorizeURL.Host+authorizeURL.Path)
	require.Equal(t, callbackURL, authorizeURL.Query().Get("redirect_uri"))
	require.Equal(t, "openid profile email", authorizeURL.Query().Get("scope"))
	require.Empty(t, authorizeURL.Query().Get("code_challenge"))
	state, stateCookie := nodeLocFlow(t, start)
	require.True(t, stateCookie.HttpOnly)
	require.Equal(t, nodeLocCallbackPath, stateCookie.Path)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, nodeLocCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	router.ServeHTTP(callback, callbackRequest)
	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_setup=nodeloc", callback.Header().Get("Location"))
	pendingCookie := nodeLocPendingCookie(t, callback)

	user, err := testRepo(h).FindByThirdPartyIdentity(context.Background(), nodeLocProvider, "42")
	require.NoError(t, err)
	require.Nil(t, user)

	codeResponse := httptest.NewRecorder()
	codeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/nodeloc/email/code", strings.NewReader(`{"mode":"new","email":"user@qq.com","turnstileToken":"valid-turnstile"}`))
	codeRequest.Header.Set("Content-Type", "application/json")
	codeRequest.AddCookie(pendingCookie)
	router.ServeHTTP(codeResponse, codeRequest)
	require.Equal(t, http.StatusNoContent, codeResponse.Code)
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)

	complete := httptest.NewRecorder()
	completeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/nodeloc/complete", strings.NewReader(`{"mode":"new","email":"user@qq.com","code":"`+code+`"}`))
	completeRequest.Header.Set("Content-Type", "application/json")
	completeRequest.AddCookie(pendingCookie)
	router.ServeHTTP(complete, completeRequest)
	require.Equal(t, http.StatusOK, complete.Code)

	user, err = testRepo(h).FindByThirdPartyIdentity(context.Background(), nodeLocProvider, "42")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, "user@qq.com", user.Email)
	for _, cookie := range complete.Result().Cookies() {
		if cookie.Name == middleware.SessionCookieName {
			session, err := h.module.SessionStore.Get(context.Background(), cookie.Value)
			require.NoError(t, err)
			require.Equal(t, user.ID, session.UserID)
			return
		}
	}
	require.Fail(t, "session cookie was not set")
}

func TestNodeLocOAuthRejectsTrustLevelBelowMinimum(t *testing.T) {
	callbackURL := configureNodeLocTest(t, 2)
	provider, _ := newNodeLocTestProvider(t, callbackURL, 1)
	h := newTestHandler()
	h.module.nodeLocClient = nodeLocTestClient(provider)
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/nodeloc", nil))
	state, stateCookie := nodeLocFlow(t, start)
	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, nodeLocCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	request.AddCookie(stateCookie)
	router.ServeHTTP(callback, request)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_error=trust_level&oauth_provider=nodeloc", callback.Header().Get("Location"))
	require.Equal(t, 0, testRepo(h).userCount())
}

func TestNodeLocOAuthCallbackRejectsInvalidStateBeforeProviderRequest(t *testing.T) {
	callbackURL := configureNodeLocTest(t, 0)
	provider, tokenCalls := newNodeLocTestProvider(t, callbackURL, 2)
	h := newTestHandler()
	h.module.nodeLocClient = nodeLocTestClient(provider)
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/nodeloc", nil))
	_, stateCookie := nodeLocFlow(t, start)
	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, nodeLocCallbackPath+"?code=authorization-code&state=wrong", nil)
	request.AddCookie(stateCookie)
	router.ServeHTTP(callback, request)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_error=state&oauth_provider=nodeloc", callback.Header().Get("Location"))
	require.Zero(t, tokenCalls.Load())
}

func TestNodeLocOAuthBindRejectsDifferentCallbackSession(t *testing.T) {
	callbackURL := configureNodeLocTest(t, 0)
	provider, tokenCalls := newNodeLocTestProvider(t, callbackURL, 2)
	h := newTestHandler()
	h.module.nodeLocClient = nodeLocTestClient(provider)
	router := setupTestRouterWithHandler(h)
	seedUserSession(t, h, "first@qq.com", "first-session")
	seedUserSession(t, h, "second@qq.com", "second-session")

	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/nodeloc/bind", nil)
	addAuthenticatedRequest(startRequest, "first-session")
	router.ServeHTTP(start, startRequest)
	state, stateCookie := nodeLocFlow(t, start)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, nodeLocCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	callbackRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "second-session"})
	router.ServeHTTP(callback, callbackRequest)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_error=session&oauth_provider=nodeloc", callback.Header().Get("Location"))
	require.Zero(t, tokenCalls.Load())
}
