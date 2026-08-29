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
	nodeLocAuthorizeEndpoint = "https://www.nodeloc.com/oauth-provider/authorize"
	nodeLocTokenEndpoint     = "https://www.nodeloc.com/oauth-provider/token"
	nodeLocUserEndpoint      = "https://www.nodeloc.com/oauth-provider/userinfo"
	nodeLocProvider          = "nodeloc"
	nodeLocStateCookieName   = "nodeloc_oauth_state"
	nodeLocPendingCookieName = "nodeloc_oauth_pending"
	nodeLocCallbackPath      = "/oauth/nodeloc"
	nodeLocPendingPath       = "/v1/oauth/nodeloc"
	nodeLocIntentLogin       = "login"
	nodeLocIntentBind        = "bind"
	nodeLocFlowMaxAge        = 10 * time.Minute
	nodeLocMaxResponseBytes  = 1 << 20
	defaultNodeLocCallback   = runtimeconfig.NodeLocCallbackURL
)

type nodeLocSettings struct {
	ClientID     string
	ClientSecret string
	CallbackURL  string
}

func currentNodeLocSettings() (nodeLocSettings, bool) {
	settings := nodeLocSettings{
		ClientID:     strings.TrimSpace(runtimeconfig.String("nodeloc_client_id", "")),
		ClientSecret: strings.TrimSpace(runtimeconfig.String("nodeloc_client_secret", "")),
		CallbackURL:  strings.TrimSpace(runtimeconfig.String("nodeloc_callback_url", defaultNodeLocCallback)),
	}
	enabled := runtimeconfig.Bool("nodeloc_oauth_enabled", false) &&
		settings.ClientID != "" && settings.ClientSecret != "" && settings.CallbackURL != ""
	return settings, enabled
}

func (h *IAMHandler) GetNodeLocAuthorize(c *gin.Context) {
	h.startNodeLoc(c, nodeLocIntentLogin)
}

func (h *IAMHandler) GetNodeLocBind(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		redirectNodeLocError(c, nodeLocIntentLogin, "session")
		return
	}
	if h.module == nil || h.module.LoginUseCase == nil {
		redirectNodeLocError(c, nodeLocIntentBind, "failed")
		return
	}
	bound, err := h.module.LoginUseCase.HasNodeLocIdentity(c.Request.Context(), userID)
	if err != nil {
		slog.Error("check nodeloc binding", "request_id", middleware.GetRequestID(c), "error", err)
		redirectNodeLocError(c, nodeLocIntentBind, "failed")
		return
	}
	if bound {
		redirectNodeLocNotice(c, "nodeloc_bound")
		return
	}
	h.startNodeLoc(c, nodeLocIntentBind)
}

func (h *IAMHandler) startNodeLoc(c *gin.Context, intent string) {
	c.Header("Cache-Control", "no-store")
	settings, enabled := currentNodeLocSettings()
	if !enabled || h.module == nil || h.module.SessionStore == nil {
		redirectNodeLocError(c, intent, "disabled")
		return
	}
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.HitOAuthStart(c.Request.Context(), nodeLocProvider, c.ClientIP())
		if err != nil {
			slog.Error("nodeloc oauth start abuse limit", "request_id", middleware.GetRequestID(c), "error", err)
			redirectNodeLocError(c, intent, "failed")
			return
		}
		if retryAfter > 0 {
			redirectNodeLocError(c, intent, "rate_limited")
			return
		}
	}

	state, err := newCSRFToken()
	if err != nil {
		slog.Error("generate nodeloc oauth state", "request_id", middleware.GetRequestID(c), "error", err)
		redirectNodeLocError(c, intent, "failed")
		return
	}
	flow := app.OAuthFlow{Provider: nodeLocProvider, Intent: intent}
	if intent == nodeLocIntentBind {
		userID, userOK := middleware.GetCurrentUserID(c)
		sessionID, sessionOK := middleware.GetCurrentSessionID(c)
		if !userOK || !sessionOK {
			redirectNodeLocError(c, nodeLocIntentLogin, "session")
			return
		}
		flow.UserID = userID
		flow.SessionID = sessionID
	}
	if err := h.module.SessionStore.PutOAuthFlow(c.Request.Context(), state, flow, nodeLocFlowMaxAge); err != nil {
		slog.Error("store nodeloc oauth flow", "request_id", middleware.GetRequestID(c), "error", err)
		redirectNodeLocError(c, intent, "failed")
		return
	}

	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(nodeLocStateCookieName, state, int(nodeLocFlowMaxAge.Seconds()), nodeLocCallbackPath, "", h.sessionSecure, true)
	authorizeURL, _ := url.Parse(nodeLocAuthorizeEndpoint)
	query := authorizeURL.Query()
	query.Set("client_id", settings.ClientID)
	query.Set("redirect_uri", settings.CallbackURL)
	query.Set("response_type", "code")
	scope := "openid profile"
	if intent == nodeLocIntentLogin {
		scope += " email"
	}
	query.Set("scope", scope)
	query.Set("state", state)
	authorizeURL.RawQuery = query.Encode()
	c.Redirect(http.StatusFound, authorizeURL.String())
}

func (h *IAMHandler) GetNodeLocCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	state := strings.TrimSpace(c.Query("state"))
	cookieState, cookieErr := c.Cookie(nodeLocStateCookieName)
	h.clearNodeLocFlowCookie(c)
	if cookieErr != nil || state == "" || len(state) > 128 || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		redirectNodeLocError(c, nodeLocIntentLogin, "state")
		return
	}
	if h.module == nil || h.module.SessionStore == nil {
		redirectNodeLocError(c, nodeLocIntentLogin, "failed")
		return
	}
	flow, err := h.module.SessionStore.ConsumeOAuthFlow(c.Request.Context(), state)
	if err != nil {
		slog.Error("consume nodeloc oauth flow", "request_id", middleware.GetRequestID(c), "error", err)
		redirectNodeLocError(c, nodeLocIntentLogin, "failed")
		return
	}
	if flow == nil || flow.Provider != nodeLocProvider || flow.Intent != nodeLocIntentLogin && flow.Intent != nodeLocIntentBind {
		redirectNodeLocError(c, nodeLocIntentLogin, "state")
		return
	}
	intent := flow.Intent
	if intent == nodeLocIntentBind {
		userID, userOK := middleware.GetCurrentUserID(c)
		sessionID, sessionOK := middleware.GetCurrentSessionID(c)
		if !userOK || !sessionOK || userID != flow.UserID || subtle.ConstantTimeCompare([]byte(sessionID), []byte(flow.SessionID)) != 1 {
			redirectNodeLocError(c, nodeLocIntentLogin, "session")
			return
		}
	}
	if strings.TrimSpace(c.Query("error")) != "" {
		redirectNodeLocError(c, intent, "cancelled")
		return
	}

	settings, enabled := currentNodeLocSettings()
	if !enabled || h.module.LoginUseCase == nil || h.module.nodeLocClient == nil {
		redirectNodeLocError(c, intent, "disabled")
		return
	}
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.HitOAuth(c.Request.Context(), nodeLocProvider, c.ClientIP())
		if err != nil {
			slog.Error("nodeloc oauth abuse limit", "request_id", middleware.GetRequestID(c), "error", err)
			redirectNodeLocError(c, intent, "failed")
			return
		}
		if retryAfter > 0 {
			redirectNodeLocError(c, intent, "rate_limited")
			return
		}
	}

	profile, err := h.module.nodeLocClient.Exchange(c.Request.Context(), strings.TrimSpace(c.Query("code")), settings)
	if err != nil {
		slog.Warn("nodeloc oauth provider request failed", "request_id", middleware.GetRequestID(c), "error", err)
		redirectNodeLocError(c, intent, "failed")
		return
	}
	if intent == nodeLocIntentBind {
		userID, _ := middleware.GetCurrentUserID(c)
		if err := h.module.LoginUseCase.BindNodeLoc(c.Request.Context(), userID, profile); err != nil {
			switch {
			case errors.Is(err, domain.ErrNodeLocIdentityAlreadyBound):
				redirectNodeLocError(c, intent, "already_bound")
			case errors.Is(err, domain.ErrNodeLocTrustLevelTooLow):
				redirectNodeLocError(c, intent, "trust_level")
			case errors.Is(err, domain.ErrNodeLocAccountUnavailable), errors.Is(err, domain.ErrThirdPartyIdentityUnavailable):
				redirectNodeLocError(c, intent, "account")
			default:
				slog.Error("bind nodeloc oauth identity", "request_id", middleware.GetRequestID(c), "error", err)
				redirectNodeLocError(c, intent, "failed")
			}
			return
		}
		redirectNodeLocNotice(c, "nodeloc_bound")
		return
	}

	result, pending, err := h.module.LoginUseCase.LoginNodeLoc(c.Request.Context(), profile, app.LoginMeta{
		ClientIP:  c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrRegistrationDisabled):
			redirectNodeLocError(c, intent, "registration_disabled")
		case errors.Is(err, domain.ErrNodeLocTrustLevelTooLow):
			redirectNodeLocError(c, intent, "trust_level")
		case errors.Is(err, domain.ErrNodeLocAccountUnavailable), errors.Is(err, domain.ErrThirdPartyIdentityUnavailable):
			redirectNodeLocError(c, intent, "account")
		default:
			slog.Error("nodeloc oauth login failed", "request_id", middleware.GetRequestID(c), "error", err)
			redirectNodeLocError(c, intent, "failed")
		}
		return
	}
	if pending != nil {
		if h.module.NodeLocPendingStore == nil {
			redirectNodeLocError(c, intent, "failed")
			return
		}
		token, err := newCSRFToken()
		if err != nil {
			redirectNodeLocError(c, intent, "failed")
			return
		}
		if err := h.module.NodeLocPendingStore.PutNodeLocPending(c.Request.Context(), token, *pending, nodeLocFlowMaxAge); err != nil {
			slog.Error("store nodeloc pending setup", "request_id", middleware.GetRequestID(c), "error", err)
			redirectNodeLocError(c, intent, "failed")
			return
		}
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(nodeLocPendingCookieName, token, int(nodeLocFlowMaxAge.Seconds()), nodeLocPendingPath, "", h.sessionSecure, true)
		c.Redirect(http.StatusSeeOther, "/login?oauth_setup=nodeloc")
		return
	}
	if result == nil {
		redirectNodeLocError(c, intent, "failed")
		return
	}
	csrfToken, err := newCSRFToken()
	if err != nil {
		redirectNodeLocError(c, intent, "failed")
		return
	}
	setAuthCookies(c, result.Session.ID, csrfToken, result.SessionMaxAge, h.sessionSecure)
	c.Redirect(http.StatusSeeOther, "/login")
}

func (h *IAMHandler) GetNodeLocPending(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_, pending, err := h.nodeLocPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, NodeLocPendingResponse{
		Provider:             nodeLocProvider,
		ProviderUserID:       pending.Profile.ID,
		Username:             nodeLocProfileName(pending.Profile),
		SuggestedEmail:       pending.SuggestedEmail,
		SuggestedEmailExists: pending.SuggestedEmailExists,
		RegistrationEnabled:  runtimeconfig.Bool("register_enabled", true),
	})
}

func (h *IAMHandler) PostNodeLocEmailCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_, pending, err := h.nodeLocPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	var req NodeLocEmailCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "fields": validationErrors(err), "requestId": middleware.GetRequestID(c)})
		return
	}
	if !h.verifyTurnstile(c, req.TurnstileToken, turnstileActionNodeLocEmail) {
		return
	}
	created, err := h.module.EmailCodeUseCase.RequestNodeLoc(c.Request.Context(), req.Email, pending.Profile.Email, req.Mode)
	if err != nil {
		writeError(c, err)
		return
	}
	if created && h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.ClearEmailCodeFailures(c.Request.Context(), req.Email); err != nil {
			slog.Warn("clear nodeloc email code abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	c.Header("Retry-After", strconv.Itoa(app.EmailCodeResendGapSeconds()))
	c.Status(http.StatusNoContent)
}

func (h *IAMHandler) PostNodeLocComplete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token, pending, err := h.nodeLocPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	var req NodeLocCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "fields": validationErrors(err), "requestId": middleware.GetRequestID(c)})
		return
	}
	clientIP := c.ClientIP()
	csrfToken, err := newCSRFToken()
	if err != nil {
		writeError(c, err)
		return
	}
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.TakeRegistration(c.Request.Context(), req.Email, clientIP)
		if err != nil {
			writeError(c, err)
			return
		}
		if retryAfter > 0 {
			writeTooManyRequests(c, retryAfter)
			return
		}
	}
	result, err := h.module.LoginUseCase.CompleteNodeLoc(c.Request.Context(), *pending, req.Mode, req.Email, req.Code, app.LoginMeta{
		ClientIP: clientIP, UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		if !errors.Is(err, domain.ErrVerificationCodeIncorrect) && h.module.AbuseLimiter != nil {
			if limitErr := h.module.AbuseLimiter.CancelRegistration(c.Request.Context(), req.Email, clientIP); limitErr != nil {
				writeError(c, limitErr)
				return
			}
		}
		writeError(c, err)
		return
	}
	if h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.CompleteRegistration(c.Request.Context(), req.Email, clientIP); err != nil {
			slog.Warn("clear nodeloc setup abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	if err := h.module.NodeLocPendingStore.DeleteNodeLocPending(c.Request.Context(), token); err != nil {
		slog.Warn("delete nodeloc pending setup", "request_id", middleware.GetRequestID(c), "error", err.Error())
	}
	h.clearNodeLocPendingCookie(c)
	setAuthCookies(c, result.Session.ID, csrfToken, result.SessionMaxAge, h.sessionSecure)
	c.JSON(http.StatusOK, LoginResponse{User: h.userResponseWithPermissions(c.Request.Context(), result.User)})
}

func (h *IAMHandler) nodeLocPending(c *gin.Context) (string, *app.NodeLocPending, error) {
	if h.module == nil || h.module.NodeLocPendingStore == nil {
		return "", nil, domain.ErrNodeLocPendingExpired
	}
	token, err := c.Cookie(nodeLocPendingCookieName)
	token = strings.TrimSpace(token)
	if err != nil || token == "" || len(token) > 128 {
		h.clearNodeLocPendingCookie(c)
		return "", nil, domain.ErrNodeLocPendingExpired
	}
	pending, err := h.module.NodeLocPendingStore.GetNodeLocPending(c.Request.Context(), token)
	if err != nil {
		return "", nil, fmt.Errorf("get nodeloc pending setup: %w", err)
	}
	if pending == nil {
		h.clearNodeLocPendingCookie(c)
		return "", nil, domain.ErrNodeLocPendingExpired
	}
	return token, pending, nil
}

func (h *IAMHandler) clearNodeLocPendingCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(nodeLocPendingCookieName, "", -1, nodeLocPendingPath, "", h.sessionSecure, true)
}

func (h *IAMHandler) clearNodeLocFlowCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(nodeLocStateCookieName, "", -1, nodeLocCallbackPath, "", h.sessionSecure, true)
}

func nodeLocProfileName(profile app.NodeLocProfile) string {
	if name := strings.TrimSpace(profile.Username); name != "" {
		return name
	}
	if name := strings.TrimSpace(profile.Name); name != "" {
		return name
	}
	return "NodeLoc User"
}

func redirectNodeLocError(c *gin.Context, intent, code string) {
	path := "/login"
	if intent == nodeLocIntentBind {
		path = "/account"
	}
	query := url.Values{"oauth_error": {code}, "oauth_provider": {nodeLocProvider}}
	c.Redirect(http.StatusSeeOther, path+"?"+query.Encode())
}

func redirectNodeLocNotice(c *gin.Context, code string) {
	c.Redirect(http.StatusSeeOther, "/account?oauth_notice="+url.QueryEscape(code))
}

type nodeLocClient struct {
	httpClient *http.Client
	tokenURL   string
	userURL    string
}

func newNodeLocClient() *nodeLocClient {
	return &nodeLocClient{
		httpClient: &http.Client{
			Timeout:       8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		tokenURL: nodeLocTokenEndpoint,
		userURL:  nodeLocUserEndpoint,
	}
}

func (client *nodeLocClient) Exchange(ctx context.Context, code string, settings nodeLocSettings) (app.NodeLocProfile, error) {
	if client == nil || client.httpClient == nil || code == "" || len(code) > 2048 || strings.ContainsAny(code, "\r\n\x00") {
		return app.NodeLocProfile{}, errors.New("invalid nodeloc authorization code")
	}
	form := url.Values{
		"client_id": {settings.ClientID}, "client_secret": {settings.ClientSecret}, "code": {code},
		"redirect_uri": {settings.CallbackURL}, "grant_type": {"authorization_code"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return app.NodeLocProfile{}, fmt.Errorf("create nodeloc token request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return app.NodeLocProfile{}, fmt.Errorf("request nodeloc token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return app.NodeLocProfile{}, fmt.Errorf("nodeloc token status %d", response.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, nodeLocMaxResponseBytes)).Decode(&token); err != nil {
		return app.NodeLocProfile{}, fmt.Errorf("decode nodeloc token: %w", err)
	}
	token.AccessToken = strings.TrimSpace(token.AccessToken)
	if token.AccessToken == "" || len(token.AccessToken) > 8192 || strings.ContainsAny(token.AccessToken, "\r\n\x00") {
		return app.NodeLocProfile{}, errors.New("nodeloc token response is invalid")
	}

	request, err = http.NewRequestWithContext(ctx, http.MethodGet, client.userURL, nil)
	if err != nil {
		return app.NodeLocProfile{}, fmt.Errorf("create nodeloc user request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err = client.httpClient.Do(request)
	if err != nil {
		return app.NodeLocProfile{}, fmt.Errorf("request nodeloc user: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return app.NodeLocProfile{}, fmt.Errorf("nodeloc user status %d", response.StatusCode)
	}
	var user struct {
		ID         json.RawMessage `json:"id"`
		Sub        string          `json:"sub"`
		Username   string          `json:"username"`
		Name       string          `json:"name"`
		Email      string          `json:"email"`
		TrustLevel int             `json:"trust_level"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, nodeLocMaxResponseBytes)).Decode(&user); err != nil {
		return app.NodeLocProfile{}, fmt.Errorf("decode nodeloc user: %w", err)
	}
	id, err := parseLinuxDOUserID(user.ID, user.Sub)
	if err != nil {
		return app.NodeLocProfile{}, domain.ErrNodeLocAccountUnavailable
	}
	return app.NodeLocProfile{ID: id, Username: user.Username, Name: user.Name, Email: user.Email, TrustLevel: user.TrustLevel}, nil
}
