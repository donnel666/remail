package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
)

const (
	githubAuthorizeEndpoint = "https://github.com/login/oauth/authorize"
	githubTokenEndpoint     = "https://github.com/login/oauth/access_token"
	githubUserEndpoint      = "https://api.github.com/user"
	githubEmailsEndpoint    = "https://api.github.com/user/emails"
	githubProvider          = "github"
	githubStateCookieName   = "github_oauth_state"
	githubPendingCookieName = "github_oauth_pending"
	githubCallbackPath      = "/v1/oauth/github/callback"
	githubPendingPath       = "/v1/oauth/github"
	githubIntentLogin       = "login"
	githubIntentBind        = "bind"
	githubFlowMaxAge        = 10 * time.Minute
	githubMaxResponseBytes  = 1 << 20
)

type githubSettings struct {
	ClientID     string
	ClientSecret string
	CallbackURL  string
}

func currentGitHubSettings() (githubSettings, bool) {
	settings := githubSettings{
		ClientID:     strings.TrimSpace(runtimeconfig.String("github_client_id", "")),
		ClientSecret: strings.TrimSpace(runtimeconfig.String("github_client_secret", "")),
		CallbackURL:  strings.TrimSpace(runtimeconfig.String("github_callback_url", "")),
	}
	enabled := runtimeconfig.Bool("github_oauth_enabled", false) &&
		settings.ClientID != "" && settings.ClientSecret != "" && settings.CallbackURL != ""
	return settings, enabled
}

func (h *IAMHandler) GetGitHubAuthorize(c *gin.Context) {
	h.startGitHub(c, githubIntentLogin)
}

func (h *IAMHandler) GetGitHubBind(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		redirectGitHubError(c, githubIntentLogin, "session")
		return
	}
	bound, err := h.module.LoginUseCase.HasGitHubIdentity(c.Request.Context(), userID)
	if err != nil {
		slog.Error("check github binding", "request_id", middleware.GetRequestID(c), "error", err)
		redirectGitHubError(c, githubIntentBind, "failed")
		return
	}
	if bound {
		redirectGitHubNotice(c, "github_bound")
		return
	}
	h.startGitHub(c, githubIntentBind)
}

func (h *IAMHandler) startGitHub(c *gin.Context, intent string) {
	c.Header("Cache-Control", "no-store")
	settings, enabled := currentGitHubSettings()
	if !enabled || h.module == nil || h.module.SessionStore == nil {
		redirectGitHubError(c, intent, "disabled")
		return
	}
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.HitOAuthStart(c.Request.Context(), githubProvider, c.ClientIP())
		if err != nil {
			slog.Error("github oauth start abuse limit", "request_id", middleware.GetRequestID(c), "error", err)
			redirectGitHubError(c, intent, "failed")
			return
		}
		if retryAfter > 0 {
			redirectGitHubError(c, intent, "rate_limited")
			return
		}
	}

	state, err := newCSRFToken()
	if err != nil {
		slog.Error("generate github oauth state", "request_id", middleware.GetRequestID(c), "error", err)
		redirectGitHubError(c, intent, "failed")
		return
	}
	flow := app.OAuthFlow{Provider: githubProvider, Intent: intent}
	if intent == githubIntentBind {
		userID, userOK := middleware.GetCurrentUserID(c)
		sessionID, sessionOK := middleware.GetCurrentSessionID(c)
		if !userOK || !sessionOK {
			redirectGitHubError(c, githubIntentLogin, "session")
			return
		}
		flow.UserID = userID
		flow.SessionID = sessionID
	}
	if err := h.module.SessionStore.PutOAuthFlow(c.Request.Context(), state, flow, githubFlowMaxAge); err != nil {
		slog.Error("store github oauth flow", "request_id", middleware.GetRequestID(c), "error", err)
		redirectGitHubError(c, intent, "failed")
		return
	}

	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(githubStateCookieName, state, int(githubFlowMaxAge.Seconds()), githubCallbackPath, "", h.sessionSecure, true)
	authorizeURL, _ := url.Parse(githubAuthorizeEndpoint)
	query := authorizeURL.Query()
	query.Set("client_id", settings.ClientID)
	query.Set("redirect_uri", settings.CallbackURL)
	query.Set("scope", "user:email")
	query.Set("state", state)
	authorizeURL.RawQuery = query.Encode()
	c.Redirect(http.StatusFound, authorizeURL.String())
}

func (h *IAMHandler) GetGitHubCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	state := strings.TrimSpace(c.Query("state"))
	cookieState, cookieErr := c.Cookie(githubStateCookieName)
	h.clearGitHubFlowCookie(c)
	if cookieErr != nil || state == "" || len(state) > 128 || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		redirectGitHubError(c, githubIntentLogin, "state")
		return
	}
	if h.module == nil || h.module.SessionStore == nil {
		redirectGitHubError(c, githubIntentLogin, "failed")
		return
	}
	flow, err := h.module.SessionStore.ConsumeOAuthFlow(c.Request.Context(), state)
	if err != nil {
		slog.Error("consume github oauth flow", "request_id", middleware.GetRequestID(c), "error", err)
		redirectGitHubError(c, githubIntentLogin, "failed")
		return
	}
	if flow == nil || flow.Provider != githubProvider || flow.Intent != githubIntentLogin && flow.Intent != githubIntentBind {
		redirectGitHubError(c, githubIntentLogin, "state")
		return
	}
	intent := flow.Intent
	if intent == githubIntentBind {
		userID, userOK := middleware.GetCurrentUserID(c)
		sessionID, sessionOK := middleware.GetCurrentSessionID(c)
		if !userOK || !sessionOK || userID != flow.UserID || subtle.ConstantTimeCompare([]byte(sessionID), []byte(flow.SessionID)) != 1 {
			redirectGitHubError(c, githubIntentLogin, "session")
			return
		}
	}
	if strings.TrimSpace(c.Query("error")) != "" {
		redirectGitHubError(c, intent, "cancelled")
		return
	}

	settings, enabled := currentGitHubSettings()
	if !enabled || h.module.LoginUseCase == nil || h.module.githubClient == nil {
		redirectGitHubError(c, intent, "disabled")
		return
	}
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.HitOAuth(c.Request.Context(), githubProvider, c.ClientIP())
		if err != nil {
			slog.Error("github oauth abuse limit", "request_id", middleware.GetRequestID(c), "error", err)
			redirectGitHubError(c, intent, "failed")
			return
		}
		if retryAfter > 0 {
			redirectGitHubError(c, intent, "rate_limited")
			return
		}
	}

	profile, err := h.module.githubClient.Exchange(c.Request.Context(), strings.TrimSpace(c.Query("code")), settings)
	if err != nil {
		slog.Warn("github oauth provider request failed", "request_id", middleware.GetRequestID(c), "error", err)
		redirectGitHubError(c, intent, "failed")
		return
	}
	if intent == githubIntentBind {
		userID, _ := middleware.GetCurrentUserID(c)
		pending, err := h.module.LoginUseCase.NewGitHubBindPending(c.Request.Context(), userID, flow.SessionID, profile)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrThirdPartyIdentityAlreadyBound):
				redirectGitHubError(c, intent, "already_bound")
			case errors.Is(err, domain.ErrGitHubVerifiedEmailUnavailable):
				redirectGitHubError(c, intent, "email")
			case errors.Is(err, domain.ErrGitHubAccountTooNew):
				redirectGitHubError(c, intent, "account_age")
			case errors.Is(err, domain.ErrGitHubAccountUnavailable), errors.Is(err, domain.ErrThirdPartyIdentityUnavailable):
				redirectGitHubError(c, intent, "account")
			default:
				slog.Error("bind github oauth identity", "request_id", middleware.GetRequestID(c), "error", err)
				redirectGitHubError(c, intent, "failed")
			}
			return
		}
		h.storeGitHubPending(c, pending, intent)
		return
	}

	result, pending, err := h.module.LoginUseCase.LoginGitHub(c.Request.Context(), profile, app.LoginMeta{
		ClientIP:  c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrRegistrationDisabled):
			redirectGitHubError(c, intent, "registration_disabled")
		case errors.Is(err, domain.ErrThirdPartyIdentityAlreadyBound):
			redirectGitHubError(c, intent, "already_bound")
		case errors.Is(err, domain.ErrGitHubVerifiedEmailUnavailable):
			redirectGitHubError(c, intent, "email")
		case errors.Is(err, domain.ErrGitHubAccountTooNew):
			redirectGitHubError(c, intent, "account_age")
		case errors.Is(err, domain.ErrGitHubAccountUnavailable), errors.Is(err, domain.ErrThirdPartyIdentityUnavailable):
			redirectGitHubError(c, intent, "account")
		default:
			slog.Error("github oauth login failed", "request_id", middleware.GetRequestID(c), "error", err)
			redirectGitHubError(c, intent, "failed")
		}
		return
	}
	if pending != nil {
		h.storeGitHubPending(c, pending, intent)
		return
	}
	if result == nil {
		redirectGitHubError(c, intent, "failed")
		return
	}
	csrfToken, err := newCSRFToken()
	if err != nil {
		redirectGitHubError(c, intent, "failed")
		return
	}
	setAuthCookies(c, result.Session.ID, csrfToken, result.SessionMaxAge, h.sessionSecure)
	c.Redirect(http.StatusSeeOther, "/login")
}

func (h *IAMHandler) storeGitHubPending(c *gin.Context, pending *app.GitHubPending, intent string) {
	if pending == nil || h.module == nil || h.module.GitHubPendingStore == nil {
		redirectGitHubError(c, intent, "failed")
		return
	}
	token, err := newCSRFToken()
	if err != nil {
		slog.Error("generate github pending token", "request_id", middleware.GetRequestID(c), "error", err)
		redirectGitHubError(c, intent, "failed")
		return
	}
	if err := h.module.GitHubPendingStore.PutGitHubPending(c.Request.Context(), token, *pending, githubFlowMaxAge); err != nil {
		slog.Error("store github pending verification", "request_id", middleware.GetRequestID(c), "error", err)
		redirectGitHubError(c, intent, "failed")
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(githubPendingCookieName, token, int(githubFlowMaxAge.Seconds()), githubPendingPath, "", h.sessionSecure, true)
	path := "/login"
	if intent == githubIntentBind {
		path = "/account"
	}
	c.Redirect(http.StatusSeeOther, path+"?oauth_setup=github")
}

func (h *IAMHandler) GetGitHubPending(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_, pending, err := h.githubPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, GitHubPendingResponse{
		Provider:       githubProvider,
		ProviderUserID: pending.Profile.ID,
		Username:       githubProfileName(pending.Profile),
		Email:          pending.Email,
		Intent:         pending.Intent,
	})
}

func (h *IAMHandler) PostGitHubEmailCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_, pending, err := h.githubPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	var req GitHubEmailCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if !h.verifyTurnstile(c, req.TurnstileToken, turnstileActionGitHubEmail) {
		return
	}
	created, err := h.module.EmailCodeUseCase.RequestGitHub(c.Request.Context(), req.Email, pending.Email)
	if err != nil {
		writeError(c, err)
		return
	}
	if created && h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.ClearEmailCodeFailures(c.Request.Context(), pending.Email); err != nil {
			slog.Warn("clear github email code abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	c.Header("Retry-After", strconv.Itoa(app.EmailCodeResendGapSeconds()))
	c.Status(http.StatusNoContent)
}

func (h *IAMHandler) PostGitHubComplete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token, pending, err := h.githubPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	var req GitHubCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	csrfToken := ""
	if pending.Intent == githubIntentLogin {
		csrfToken, err = newCSRFToken()
		if err != nil {
			writeError(c, err)
			return
		}
	}

	clientIP := c.ClientIP()
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.TakeRegistration(c.Request.Context(), pending.Email, clientIP)
		if err != nil {
			writeError(c, err)
			return
		}
		if retryAfter > 0 {
			writeTooManyRequests(c, retryAfter)
			return
		}
	}
	result, err := h.module.LoginUseCase.CompleteGitHub(c.Request.Context(), *pending, req.Code, app.LoginMeta{
		ClientIP:  clientIP,
		UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		if !errors.Is(err, domain.ErrVerificationCodeIncorrect) && h.module.AbuseLimiter != nil {
			if limitErr := h.module.AbuseLimiter.CancelRegistration(c.Request.Context(), pending.Email, clientIP); limitErr != nil {
				writeError(c, limitErr)
				return
			}
		}
		writeError(c, err)
		return
	}
	if h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.CompleteRegistration(c.Request.Context(), pending.Email, clientIP); err != nil {
			slog.Warn("clear github verification abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	if err := h.module.GitHubPendingStore.DeleteGitHubPending(c.Request.Context(), token); err != nil {
		slog.Warn("delete github pending verification", "request_id", middleware.GetRequestID(c), "error", err.Error())
	}
	h.clearGitHubPendingCookie(c)
	if pending.Intent == githubIntentBind {
		c.Status(http.StatusNoContent)
		return
	}
	if result == nil {
		writeError(c, domain.ErrGitHubAccountUnavailable)
		return
	}
	setAuthCookies(c, result.Session.ID, csrfToken, result.SessionMaxAge, h.sessionSecure)
	c.JSON(http.StatusOK, LoginResponse{User: h.userResponseWithPermissions(c.Request.Context(), result.User)})
}

func (h *IAMHandler) githubPending(c *gin.Context) (string, *app.GitHubPending, error) {
	if h.module == nil || h.module.GitHubPendingStore == nil {
		return "", nil, domain.ErrGitHubPendingExpired
	}
	token, err := c.Cookie(githubPendingCookieName)
	token = strings.TrimSpace(token)
	if err != nil || token == "" || len(token) > 128 {
		h.clearGitHubPendingCookie(c)
		return "", nil, domain.ErrGitHubPendingExpired
	}
	pending, err := h.module.GitHubPendingStore.GetGitHubPending(c.Request.Context(), token)
	if err != nil {
		return "", nil, fmt.Errorf("get github pending verification: %w", err)
	}
	if pending == nil || pending.Intent != githubIntentLogin && pending.Intent != githubIntentBind {
		h.clearGitHubPendingCookie(c)
		return "", nil, domain.ErrGitHubPendingExpired
	}
	if pending.Intent == githubIntentBind {
		userID, userOK := middleware.GetCurrentUserID(c)
		sessionID, sessionOK := middleware.GetCurrentSessionID(c)
		if !userOK || !sessionOK || userID != pending.UserID || subtle.ConstantTimeCompare([]byte(sessionID), []byte(pending.SessionID)) != 1 {
			return "", nil, domain.ErrGitHubPendingExpired
		}
	}
	return token, pending, nil
}

func (h *IAMHandler) clearGitHubPendingCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(githubPendingCookieName, "", -1, githubPendingPath, "", h.sessionSecure, true)
}

func githubProfileName(profile app.GitHubProfile) string {
	if name := strings.TrimSpace(profile.Username); name != "" {
		return name
	}
	if name := strings.TrimSpace(profile.Name); name != "" {
		return name
	}
	return "GitHub User"
}

func (h *IAMHandler) clearGitHubFlowCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(githubStateCookieName, "", -1, githubCallbackPath, "", h.sessionSecure, true)
}

func redirectGitHubError(c *gin.Context, intent, code string) {
	path := "/login"
	if intent == githubIntentBind {
		path = "/account"
	}
	query := url.Values{"oauth_error": {code}, "oauth_provider": {githubProvider}}
	c.Redirect(http.StatusSeeOther, path+"?"+query.Encode())
}

func redirectGitHubNotice(c *gin.Context, code string) {
	c.Redirect(http.StatusSeeOther, "/account?oauth_notice="+url.QueryEscape(code))
}

type githubClient struct {
	httpClient *http.Client
	tokenURL   string
	userURL    string
	emailsURL  string
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func newGitHubClient() *githubClient {
	return &githubClient{
		httpClient: &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		tokenURL:  githubTokenEndpoint,
		userURL:   githubUserEndpoint,
		emailsURL: githubEmailsEndpoint,
	}
}

func (client *githubClient) Exchange(ctx context.Context, code string, settings githubSettings) (app.GitHubProfile, error) {
	if client == nil || client.httpClient == nil || code == "" || len(code) > 2048 || strings.ContainsAny(code, "\r\n\x00") {
		return app.GitHubProfile{}, errors.New("invalid github authorization code")
	}
	form := url.Values{
		"client_id":     {settings.ClientID},
		"client_secret": {settings.ClientSecret},
		"code":          {code},
		"redirect_uri":  {settings.CallbackURL},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return app.GitHubProfile{}, fmt.Errorf("create github token request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "Remail")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return app.GitHubProfile{}, fmt.Errorf("request github token: %w", err)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	decodeErr := decodeGitHubJSON(response, &token)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return app.GitHubProfile{}, fmt.Errorf("github token status %d", response.StatusCode)
	}
	if decodeErr != nil {
		return app.GitHubProfile{}, fmt.Errorf("decode github token: %w", decodeErr)
	}
	token.AccessToken = strings.TrimSpace(token.AccessToken)
	if token.Error != "" || token.AccessToken == "" || len(token.AccessToken) > 8192 || strings.ContainsAny(token.AccessToken, "\r\n\x00") {
		return app.GitHubProfile{}, errors.New("github token response is invalid")
	}

	var user struct {
		ID          uint64    `json:"id"`
		Login       string    `json:"login"`
		Name        string    `json:"name"`
		CreatedAt   time.Time `json:"created_at"`
		SuspendedAt *string   `json:"suspended_at"`
	}
	if err := client.get(ctx, client.userURL, token.AccessToken, &user); err != nil {
		return app.GitHubProfile{}, err
	}
	if user.ID == 0 || user.SuspendedAt != nil {
		return app.GitHubProfile{}, domain.ErrGitHubAccountUnavailable
	}
	var emails []githubEmail
	if err := client.get(ctx, client.emailsURL, token.AccessToken, &emails); err != nil {
		return app.GitHubProfile{}, err
	}
	return app.GitHubProfile{
		ID:        strconv.FormatUint(user.ID, 10),
		Username:  user.Login,
		Name:      user.Name,
		Email:     selectGitHubVerifiedEmail(emails),
		CreatedAt: user.CreatedAt,
	}, nil
}

func (client *githubClient) get(ctx context.Context, endpoint, accessToken string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create github api request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("User-Agent", "Remail")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request github api: %w", err)
	}
	decodeErr := decodeGitHubJSON(response, target)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("github api status %d", response.StatusCode)
	}
	if decodeErr != nil {
		return fmt.Errorf("decode github api response: %w", decodeErr)
	}
	return nil
}

func decodeGitHubJSON(response *http.Response, target any) error {
	defer response.Body.Close()
	return json.NewDecoder(io.LimitReader(response.Body, githubMaxResponseBytes)).Decode(target)
}

func selectGitHubVerifiedEmail(emails []githubEmail) string {
	fallback := ""
	for _, email := range emails {
		if !email.Verified {
			continue
		}
		if email.Primary {
			return strings.TrimSpace(email.Email)
		}
		if fallback == "" {
			fallback = strings.TrimSpace(email.Email)
		}
	}
	return fallback
}
