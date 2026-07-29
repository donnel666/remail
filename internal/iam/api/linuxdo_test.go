package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func configureLinuxDOTest(t *testing.T) string {
	t.Helper()
	before := runtimeconfig.Snapshot()
	t.Cleanup(func() {
		settings := make([]settingsdomain.Setting, 0, len(before))
		for key, value := range before {
			settings = append(settings, settingsdomain.Setting{Key: key, Value: value})
		}
		runtimeconfig.Replace(settings)
	})

	callbackURL := "https://remail.example.com/v1/oauth/linuxdo/callback"
	runtimeconfig.SetMany([]settingsdomain.Setting{
		{Key: "register_enabled", Value: "true"},
		{Key: "registration_reward_amount", Value: "0"},
		{Key: "linuxdo_oauth_enabled", Value: "true"},
		{Key: "linuxdo_client_id", Value: "client-id"},
		{Key: "linuxdo_client_secret", Value: "client-secret"},
		{Key: "linuxdo_callback_url", Value: callbackURL},
		{Key: "linuxdo_minimum_trust_level", Value: "0"},
	})
	return callbackURL
}

func newLinuxDOTestProvider(t *testing.T, linuxDOID string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	tokenCalls := &atomic.Int32{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			if r.ParseForm() != nil || len(r.Form.Get("code_verifier")) < 43 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-token"}`))
		case "/user":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":` + linuxDOID + `,"username":"linuxdo-user","name":"LinuxDo User","active":true,"trust_level":2}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	return provider, tokenCalls
}

func linuxDOTestClient(provider *httptest.Server) *linuxDOClient {
	return &linuxDOClient{
		httpClient: provider.Client(),
		tokenURL:   provider.URL + "/token",
		userURL:    provider.URL + "/user",
	}
}

func TestParseLinuxDOUserIDFallsBackToOIDCSubject(t *testing.T) {
	id, err := parseLinuxDOUserID(nil, "42")
	require.NoError(t, err)
	require.Equal(t, "42", id)
}

func linuxDOFlow(t *testing.T, response *httptest.ResponseRecorder) (string, *http.Cookie) {
	t.Helper()
	authorizeURL, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	state := authorizeURL.Query().Get("state")
	require.NotEmpty(t, state)
	var stateCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == linuxDOStateCookieName {
			stateCookie = cookie
		}
	}
	require.NotNil(t, stateCookie)
	return state, stateCookie
}

func TestLinuxDOOAuthCallbackRequiresVerifiedAccountOwnership(t *testing.T) {
	before := runtimeconfig.Snapshot()
	t.Cleanup(func() {
		settings := make([]settingsdomain.Setting, 0, len(before))
		for key, value := range before {
			settings = append(settings, settingsdomain.Setting{Key: key, Value: value})
		}
		runtimeconfig.Replace(settings)
	})

	callbackURL := "https://remail.example.com/v1/oauth/linuxdo/callback"
	runtimeconfig.SetMany([]settingsdomain.Setting{
		{Key: "register_enabled", Value: "true"},
		{Key: "linuxdo_oauth_enabled", Value: "true"},
		{Key: "linuxdo_client_id", Value: "client-id"},
		{Key: "linuxdo_client_secret", Value: "client-secret"},
		{Key: "linuxdo_callback_url", Value: callbackURL},
		{Key: "linuxdo_minimum_trust_level", Value: "2"},
	})

	codeChallenge := ""
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Method != http.MethodPost || r.ParseForm() != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			verifier := r.Form.Get("code_verifier")
			challenge := sha256.Sum256([]byte(verifier))
			if r.Form.Get("client_id") != "client-id" ||
				r.Form.Get("client_secret") != "client-secret" ||
				r.Form.Get("code") != "authorization-code" ||
				base64.RawURLEncoding.EncodeToString(challenge[:]) != codeChallenge ||
				r.Form.Get("redirect_uri") != callbackURL ||
				r.Form.Get("grant_type") != "authorization_code" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-token"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer access-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":42,"username":"linuxdo-user","name":"LinuxDo User","active":true,"trust_level":2}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()

	h := newTestHandler()
	h.module.linuxDOClient = &linuxDOClient{
		httpClient: provider.Client(),
		tokenURL:   provider.URL + "/token",
		userURL:    provider.URL + "/user",
	}
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo", nil))
	require.Equal(t, http.StatusFound, start.Code)
	authorizeURL, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, linuxDOAuthorizeEndpoint, authorizeURL.Scheme+"://"+authorizeURL.Host+authorizeURL.Path)
	require.Equal(t, "client-id", authorizeURL.Query().Get("client_id"))
	require.Equal(t, callbackURL, authorizeURL.Query().Get("redirect_uri"))
	require.Equal(t, "code", authorizeURL.Query().Get("response_type"))
	require.Equal(t, "openid profile email", authorizeURL.Query().Get("scope"))
	require.Equal(t, "S256", authorizeURL.Query().Get("code_challenge_method"))
	codeChallenge = authorizeURL.Query().Get("code_challenge")
	require.NotEmpty(t, codeChallenge)
	state := authorizeURL.Query().Get("state")
	require.NotEmpty(t, state)

	var stateCookie *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == linuxDOStateCookieName {
			stateCookie = cookie
		}
	}
	require.NotNil(t, stateCookie)
	require.True(t, stateCookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, stateCookie.SameSite)
	require.Equal(t, linuxDOCallbackPath, stateCookie.Path)

	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, linuxDOCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	request.AddCookie(stateCookie)
	router.ServeHTTP(callback, request)
	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_setup=linuxdo", callback.Header().Get("Location"))

	user, err := testRepo(h).FindByLinuxDOID(context.Background(), "42")
	require.NoError(t, err)
	require.Nil(t, user)

	var pendingCookie *http.Cookie
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == linuxDOPendingCookieName {
			pendingCookie = cookie
		}
		require.NotEqual(t, middleware.SessionCookieName, cookie.Name)
	}
	require.NotNil(t, pendingCookie)
	require.True(t, pendingCookie.HttpOnly)
	require.Equal(t, linuxDOPendingPath, pendingCookie.Path)

	pendingResponse := httptest.NewRecorder()
	pendingRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo/pending", nil)
	pendingRequest.AddCookie(pendingCookie)
	router.ServeHTTP(pendingResponse, pendingRequest)
	require.Equal(t, http.StatusOK, pendingResponse.Code)
	require.Contains(t, pendingResponse.Body.String(), `"providerUserId":"42"`)

	codeResponse := httptest.NewRecorder()
	codeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/linuxdo/email/code", strings.NewReader(`{"mode":"new","email":"user@qq.com","turnstileToken":"valid-turnstile"}`))
	codeRequest.Header.Set("Content-Type", "application/json")
	codeRequest.AddCookie(pendingCookie)
	router.ServeHTTP(codeResponse, codeRequest)
	require.Equal(t, http.StatusNoContent, codeResponse.Code)
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)

	completeResponse := httptest.NewRecorder()
	completeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/linuxdo/complete", strings.NewReader(`{"mode":"new","email":"user@qq.com","code":"`+code+`"}`))
	completeRequest.Header.Set("Content-Type", "application/json")
	completeRequest.AddCookie(pendingCookie)
	router.ServeHTTP(completeResponse, completeRequest)
	require.Equal(t, http.StatusOK, completeResponse.Code)

	user, err = testRepo(h).FindByLinuxDOID(context.Background(), "42")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, "user@qq.com", user.Email)
	require.Equal(t, "LinuxDo User", user.Nickname)

	var sessionID string
	for _, cookie := range completeResponse.Result().Cookies() {
		if cookie.Name == middleware.SessionCookieName {
			sessionID = cookie.Value
		}
	}
	require.NotEmpty(t, sessionID)
	session, err := h.module.SessionStore.Get(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, user.ID, session.UserID)
}

func TestLinuxDOOAuthBindsAuthenticatedUserWithoutReplacingSession(t *testing.T) {
	configureLinuxDOTest(t)
	provider, tokenCalls := newLinuxDOTestProvider(t, "77")
	h := newTestHandler()
	h.module.linuxDOClient = linuxDOTestClient(provider)
	router := setupTestRouterWithHandler(h)
	user := seedUserSession(t, h, "user@qq.com", "user-session")

	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo/bind", nil)
	addAuthenticatedRequest(startRequest, "user-session")
	router.ServeHTTP(start, startRequest)
	require.Equal(t, http.StatusFound, start.Code)
	authorizeURL, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "openid profile", authorizeURL.Query().Get("scope"))
	state, stateCookie := linuxDOFlow(t, start)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, linuxDOCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	callbackRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "user-session"})
	router.ServeHTTP(callback, callbackRequest)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/account?oauth_notice=linuxdo_bound", callback.Header().Get("Location"))
	require.EqualValues(t, 1, tokenCalls.Load())
	require.Equal(t, 1, testRepo(h).userCount())
	bound, err := testRepo(h).HasLinuxDOIdentity(context.Background(), user.ID)
	require.NoError(t, err)
	require.True(t, bound)
	session, err := h.module.SessionStore.Get(context.Background(), "user-session")
	require.NoError(t, err)
	require.NotNil(t, session)
	for _, cookie := range callback.Result().Cookies() {
		require.NotEqual(t, middleware.SessionCookieName, cookie.Name)
	}

	config := httptest.NewRecorder()
	configRequest := httptest.NewRequest(http.MethodGet, "/v1/login/config", nil)
	configRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "user-session"})
	router.ServeHTTP(config, configRequest)
	require.Equal(t, http.StatusOK, config.Code)
	require.Contains(t, config.Body.String(), `"linuxdoBound":true`)
}

func TestLinuxDOOAuthBindRequiresLiveSessionBeforeProviderRequest(t *testing.T) {
	configureLinuxDOTest(t)
	provider, tokenCalls := newLinuxDOTestProvider(t, "77")
	h := newTestHandler()
	h.module.linuxDOClient = linuxDOTestClient(provider)
	router := setupTestRouterWithHandler(h)
	seedUserSession(t, h, "user@qq.com", "user-session")

	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo/bind", nil)
	addAuthenticatedRequest(startRequest, "user-session")
	router.ServeHTTP(start, startRequest)
	state, stateCookie := linuxDOFlow(t, start)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, linuxDOCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	router.ServeHTTP(callback, callbackRequest)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_error=session", callback.Header().Get("Location"))
	require.Zero(t, tokenCalls.Load())
}

func TestLinuxDOOAuthBindStartRedirectsExpiredSessionToLogin(t *testing.T) {
	configureLinuxDOTest(t)
	h := newTestHandler()
	router := setupTestRouterWithHandler(h)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo/bind", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "expired-session"})
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusSeeOther, response.Code)
	require.Equal(t, "/login?oauth_error=session", response.Header().Get("Location"))
}

func TestLinuxDOOAuthBindRejectsDifferentCallbackSessionBeforeProviderRequest(t *testing.T) {
	configureLinuxDOTest(t)
	provider, tokenCalls := newLinuxDOTestProvider(t, "77")
	h := newTestHandler()
	h.module.linuxDOClient = linuxDOTestClient(provider)
	router := setupTestRouterWithHandler(h)
	first := seedUserSession(t, h, "first@qq.com", "first-session")
	second := seedUserSession(t, h, "second@qq.com", "second-session")

	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo/bind", nil)
	addAuthenticatedRequest(startRequest, "first-session")
	router.ServeHTTP(start, startRequest)
	state, stateCookie := linuxDOFlow(t, start)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, linuxDOCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	callbackRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "second-session"})
	router.ServeHTTP(callback, callbackRequest)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_error=session", callback.Header().Get("Location"))
	require.Zero(t, tokenCalls.Load())
	firstBound, err := testRepo(h).HasLinuxDOIdentity(context.Background(), first.ID)
	require.NoError(t, err)
	secondBound, err := testRepo(h).HasLinuxDOIdentity(context.Background(), second.ID)
	require.NoError(t, err)
	require.False(t, firstBound)
	require.False(t, secondBound)
}

func TestLinuxDOOAuthBindRejectsIdentityOwnedByAnotherUser(t *testing.T) {
	configureLinuxDOTest(t)
	provider, _ := newLinuxDOTestProvider(t, "77")
	h := newTestHandler()
	h.module.linuxDOClient = linuxDOTestClient(provider)
	router := setupTestRouterWithHandler(h)
	seedUserSession(t, h, "first@qq.com", "first-session")
	other := seedUser(t, h, "other@qq.com")
	require.NoError(t, testRepo(h).BindLinuxDOIdentity(context.Background(), other.ID, "77"))

	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo/bind", nil)
	addAuthenticatedRequest(startRequest, "first-session")
	router.ServeHTTP(start, startRequest)
	state, stateCookie := linuxDOFlow(t, start)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, linuxDOCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	callbackRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "first-session"})
	router.ServeHTTP(callback, callbackRequest)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/account?oauth_error=already_bound", callback.Header().Get("Location"))
	require.Equal(t, 2, testRepo(h).userCount())
}

func TestLinuxDOOAuthCallbackRejectsInvalidStateBeforeProviderRequest(t *testing.T) {
	configureLinuxDOTest(t)
	provider, tokenCalls := newLinuxDOTestProvider(t, "77")
	h := newTestHandler()
	h.module.linuxDOClient = linuxDOTestClient(provider)
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo", nil))
	_, stateCookie := linuxDOFlow(t, start)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, linuxDOCallbackPath+"?code=authorization-code&state=wrong", nil)
	callbackRequest.AddCookie(stateCookie)
	router.ServeHTTP(callback, callbackRequest)

	require.Equal(t, "/login?oauth_error=state", callback.Header().Get("Location"))
	require.Zero(t, tokenCalls.Load())
}

func TestLinuxDOOAuthCallbackRateLimitSkipsProviderRequest(t *testing.T) {
	configureLinuxDOTest(t)
	provider, tokenCalls := newLinuxDOTestProvider(t, "77")
	h := newTestHandler()
	h.module.linuxDOClient = linuxDOTestClient(provider)
	limiter := &fakeAbuseLimiter{linuxDORetry: 60}
	h.module.AbuseLimiter = limiter
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/linuxdo", nil))
	state, stateCookie := linuxDOFlow(t, start)

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, linuxDOCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	router.ServeHTTP(callback, callbackRequest)

	require.Equal(t, "/login?oauth_error=rate_limited", callback.Header().Get("Location"))
	require.Equal(t, 1, limiter.linuxDOHits)
	require.Zero(t, tokenCalls.Load())
}
