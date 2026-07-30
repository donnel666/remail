package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/domain"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func configureGitHubTest(t *testing.T, minimumAgeDays int) string {
	t.Helper()
	before := runtimeconfig.Snapshot()
	t.Cleanup(func() {
		settings := make([]settingsdomain.Setting, 0, len(before))
		for key, value := range before {
			settings = append(settings, settingsdomain.Setting{Key: key, Value: value})
		}
		runtimeconfig.Replace(settings)
	})
	callbackURL := "https://remail.example.com/v1/oauth/github/callback"
	runtimeconfig.SetMany([]settingsdomain.Setting{
		{Key: "register_enabled", Value: "true"},
		{Key: "registration_reward_amount", Value: "0"},
		{Key: "github_oauth_enabled", Value: "true"},
		{Key: "github_client_id", Value: "client-id"},
		{Key: "github_client_secret", Value: "client-secret"},
		{Key: "github_callback_url", Value: callbackURL},
		{Key: "github_minimum_account_age_days", Value: strconv.Itoa(minimumAgeDays)},
	})
	return callbackURL
}

func newGitHubTestProvider(t *testing.T, callbackURL string, createdAt time.Time) *httptest.Server {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Method != http.MethodPost || r.ParseForm() != nil ||
				r.Form.Get("client_id") != "client-id" ||
				r.Form.Get("client_secret") != "client-secret" ||
				r.Form.Get("code") != "authorization-code" ||
				r.Form.Get("redirect_uri") != callbackURL {
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
			_, _ = w.Write([]byte(`{"id":42,"login":"octocat","name":"Octo Cat","created_at":"` + createdAt.UTC().Format(time.RFC3339) + `"}`))
		case "/emails":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"email":"unverified@example.com","primary":true,"verified":false},{"email":"owner@example.com","primary":false,"verified":true}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	return provider
}

func githubTestClient(provider *httptest.Server) *githubClient {
	return &githubClient{
		httpClient: provider.Client(),
		tokenURL:   provider.URL + "/token",
		userURL:    provider.URL + "/user",
		emailsURL:  provider.URL + "/emails",
	}
}

func githubFlow(t *testing.T, response *httptest.ResponseRecorder) (string, *http.Cookie) {
	t.Helper()
	authorizeURL, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	state := authorizeURL.Query().Get("state")
	require.NotEmpty(t, state)
	var stateCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == githubStateCookieName {
			stateCookie = cookie
		}
	}
	require.NotNil(t, stateCookie)
	return state, stateCookie
}

func githubPendingCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == githubPendingCookieName && cookie.Value != "" {
			return cookie
		}
	}
	require.FailNow(t, "github pending cookie was not set")
	return nil
}

func TestGitHubOAuthLoginUsesVerifiedEmail(t *testing.T) {
	callbackURL := configureGitHubTest(t, 365)
	provider := newGitHubTestProvider(t, callbackURL, time.Now().AddDate(-2, 0, 0))
	h := newTestHandler()
	h.module.githubClient = githubTestClient(provider)
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/github", nil))
	require.Equal(t, http.StatusFound, start.Code)
	authorizeURL, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, githubAuthorizeEndpoint, authorizeURL.Scheme+"://"+authorizeURL.Host+authorizeURL.Path)
	require.Equal(t, "client-id", authorizeURL.Query().Get("client_id"))
	require.Equal(t, callbackURL, authorizeURL.Query().Get("redirect_uri"))
	require.Equal(t, "user:email", authorizeURL.Query().Get("scope"))
	state, stateCookie := githubFlow(t, start)
	require.True(t, stateCookie.HttpOnly)
	require.Equal(t, githubCallbackPath, stateCookie.Path)

	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, githubCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	request.AddCookie(stateCookie)
	router.ServeHTTP(callback, request)
	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login", callback.Header().Get("Location"))

	user, err := testRepo(h).FindByThirdPartyIdentity(context.Background(), githubProvider, "42")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, "owner@example.com", user.Email)
	require.Equal(t, "Octo Cat", user.Nickname)

	var sessionID string
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == middleware.SessionCookieName {
			sessionID = cookie.Value
		}
	}
	require.NotEmpty(t, sessionID)
	session, err := h.module.SessionStore.Get(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, user.ID, session.UserID)
}

func TestGitHubOAuthRejectsAccountBelowMinimumAge(t *testing.T) {
	callbackURL := configureGitHubTest(t, 365)
	provider := newGitHubTestProvider(t, callbackURL, time.Now().AddDate(0, -3, 0))
	h := newTestHandler()
	h.module.githubClient = githubTestClient(provider)
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/github", nil))
	state, stateCookie := githubFlow(t, start)

	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, githubCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	request.AddCookie(stateCookie)
	router.ServeHTTP(callback, request)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_error=account_age&oauth_provider=github", callback.Header().Get("Location"))
	require.Equal(t, 0, testRepo(h).userCount())
}

func TestGitHubOAuthExistingAccountRequiresCurrentEmailVerification(t *testing.T) {
	callbackURL := configureGitHubTest(t, 365)
	provider := newGitHubTestProvider(t, callbackURL, time.Now().AddDate(-2, 0, 0))
	h := newTestHandler()
	h.module.githubClient = githubTestClient(provider)
	existing := seedUser(t, h, "owner@example.com")
	existing.Role = domain.RoleAdmin
	require.NoError(t, testRepo(h).Update(context.Background(), existing))
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v1/oauth/github", nil))
	state, stateCookie := githubFlow(t, start)
	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, githubCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	request.AddCookie(stateCookie)
	router.ServeHTTP(callback, request)

	require.Equal(t, http.StatusSeeOther, callback.Code)
	require.Equal(t, "/login?oauth_setup=github", callback.Header().Get("Location"))
	pendingCookie := githubPendingCookie(t, callback)
	bound, err := testRepo(h).HasThirdPartyIdentity(context.Background(), existing.ID, githubProvider)
	require.NoError(t, err)
	require.False(t, bound)
	for _, cookie := range callback.Result().Cookies() {
		require.False(t, cookie.Name == middleware.SessionCookieName && cookie.Value != "")
	}

	pending := httptest.NewRecorder()
	pendingRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/github/pending", nil)
	pendingRequest.AddCookie(pendingCookie)
	router.ServeHTTP(pending, pendingRequest)
	require.Equal(t, http.StatusOK, pending.Code)
	require.Contains(t, pending.Body.String(), `"email":"owner@example.com"`)
	require.Contains(t, pending.Body.String(), `"intent":"login"`)

	send := httptest.NewRecorder()
	sendRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/github/email/code", strings.NewReader(`{"email":"owner@example.com","turnstileToken":"test-token"}`))
	sendRequest.Header.Set("Content-Type", "application/json")
	sendRequest.AddCookie(pendingCookie)
	router.ServeHTTP(send, sendRequest)
	require.Equal(t, http.StatusNoContent, send.Code)
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()
	require.NotEmpty(t, code)

	complete := httptest.NewRecorder()
	completeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/github/complete", strings.NewReader(fmt.Sprintf(`{"code":%q}`, code)))
	completeRequest.Header.Set("Content-Type", "application/json")
	completeRequest.AddCookie(pendingCookie)
	router.ServeHTTP(complete, completeRequest)
	require.Equal(t, http.StatusOK, complete.Code)
	bound, err = testRepo(h).HasThirdPartyIdentity(context.Background(), existing.ID, githubProvider)
	require.NoError(t, err)
	require.True(t, bound)
	user, err := testRepo(h).FindByThirdPartyIdentity(context.Background(), githubProvider, "42")
	require.NoError(t, err)
	require.Equal(t, domain.RoleAdmin, user.Role)
}

func TestGitHubOAuthBindRequiresSameSessionAndEmailVerification(t *testing.T) {
	callbackURL := configureGitHubTest(t, 365)
	provider := newGitHubTestProvider(t, callbackURL, time.Now().AddDate(-2, 0, 0))
	h := newTestHandler()
	h.module.githubClient = githubTestClient(provider)
	user := seedUserSession(t, h, "local@example.com", "binding-session")
	router := setupTestRouterWithHandler(h)

	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/github/bind", nil)
	addAuthenticatedRequest(startRequest, "binding-session")
	router.ServeHTTP(start, startRequest)
	state, stateCookie := githubFlow(t, start)
	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, githubCallbackPath+"?code=authorization-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(stateCookie)
	callbackRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "binding-session"})
	router.ServeHTTP(callback, callbackRequest)
	require.Equal(t, "/account?oauth_setup=github", callback.Header().Get("Location"))
	pendingCookie := githubPendingCookie(t, callback)

	stolen := httptest.NewRecorder()
	stolenRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/github/complete", strings.NewReader(`{"code":"123456"}`))
	stolenRequest.Header.Set("Content-Type", "application/json")
	stolenRequest.AddCookie(pendingCookie)
	router.ServeHTTP(stolen, stolenRequest)
	require.Equal(t, http.StatusUnauthorized, stolen.Code)
	bound, err := testRepo(h).HasThirdPartyIdentity(context.Background(), user.ID, githubProvider)
	require.NoError(t, err)
	require.False(t, bound)

	send := httptest.NewRecorder()
	sendRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/github/email/code", strings.NewReader(`{"email":"local@example.com","turnstileToken":"test-token"}`))
	sendRequest.Header.Set("Content-Type", "application/json")
	sendRequest.AddCookie(pendingCookie)
	sendRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "binding-session"})
	router.ServeHTTP(send, sendRequest)
	require.Equal(t, http.StatusNoContent, send.Code)
	code := h.module.EmailCodeStore.(*mockEmailCodeStore).firstCode()

	complete := httptest.NewRecorder()
	completeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/github/complete", strings.NewReader(fmt.Sprintf(`{"code":%q}`, code)))
	completeRequest.Header.Set("Content-Type", "application/json")
	completeRequest.AddCookie(pendingCookie)
	completeRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "binding-session"})
	router.ServeHTTP(complete, completeRequest)
	require.Equal(t, http.StatusNoContent, complete.Code)
	bound, err = testRepo(h).HasThirdPartyIdentity(context.Background(), user.ID, githubProvider)
	require.NoError(t, err)
	require.True(t, bound)
}

func TestGitHubOAuthStartRateLimitDoesNotStoreFlow(t *testing.T) {
	configureGitHubTest(t, 0)
	h := newTestHandler()
	limiter := &fakeAbuseLimiter{oauthStartRetry: 60}
	h.module.AbuseLimiter = limiter
	router := setupTestRouterWithHandler(h)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/oauth/github", nil))

	require.Equal(t, http.StatusSeeOther, response.Code)
	require.Equal(t, "/login?oauth_error=rate_limited&oauth_provider=github", response.Header().Get("Location"))
	require.Equal(t, 1, limiter.oauthStartHits)
	store := h.module.SessionStore.(*mockSessionStore)
	store.mu.Lock()
	flowCount := len(store.oauthFlows)
	store.mu.Unlock()
	require.Zero(t, flowCount)
}
