package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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
	linuxDOAuthorizeEndpoint = "https://connect.linux.do/oauth2/authorize"
	linuxDOTokenEndpoint     = "https://connect.linux.do/oauth2/token"
	linuxDOUserEndpoint      = "https://connect.linux.do/api/user"
	linuxDOProvider          = "linuxdo"
	linuxDOStateCookieName   = "linuxdo_oauth_state"
	linuxDOPendingCookieName = "linuxdo_oauth_pending"
	linuxDOCallbackPath      = "/v1/oauth/linuxdo/callback"
	linuxDOPendingPath       = "/v1/oauth/linuxdo"
	linuxDOIntentLogin       = "login"
	linuxDOIntentBind        = "bind"
	linuxDOFlowMaxAge        = 10 * time.Minute
	linuxDOMaxResponseBytes  = 1 << 20
)

type linuxDOSettings struct {
	ClientID     string
	ClientSecret string
	CallbackURL  string
}

func currentLinuxDOSettings() (linuxDOSettings, bool) {
	settings := linuxDOSettings{
		ClientID:     strings.TrimSpace(runtimeconfig.String("linuxdo_client_id", "")),
		ClientSecret: strings.TrimSpace(runtimeconfig.String("linuxdo_client_secret", "")),
		CallbackURL:  strings.TrimSpace(runtimeconfig.String("linuxdo_callback_url", "")),
	}
	enabled := runtimeconfig.Bool("linuxdo_oauth_enabled", false) &&
		settings.ClientID != "" && settings.ClientSecret != "" && settings.CallbackURL != ""
	return settings, enabled
}

func (h *IAMHandler) GetLoginConfig(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_, linuxDOEnabled := currentLinuxDOSettings()
	_, githubEnabled := currentGitHubSettings()
	_, nodeLocEnabled := currentNodeLocSettings()
	linuxDOBound := false
	githubBound := false
	nodeLocBound := false
	if userID, ok := middleware.GetCurrentUserID(c); ok && h.module != nil && h.module.LoginUseCase != nil {
		var err error
		linuxDOBound, err = h.module.LoginUseCase.HasLinuxDOIdentity(c.Request.Context(), userID)
		if err != nil {
			writeError(c, err)
			return
		}
		githubBound, err = h.module.LoginUseCase.HasGitHubIdentity(c.Request.Context(), userID)
		if err != nil {
			writeError(c, err)
			return
		}
		nodeLocBound, err = h.module.LoginUseCase.HasNodeLocIdentity(c.Request.Context(), userID)
		if err != nil {
			writeError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, LoginConfigResponse{
		LinuxDOOAuthEnabled: linuxDOEnabled,
		LinuxDOBound:        linuxDOBound,
		GitHubOAuthEnabled:  githubEnabled,
		GitHubBound:         githubBound,
		NodeLocOAuthEnabled: nodeLocEnabled,
		NodeLocBound:        nodeLocBound,
	})
}

func (h *IAMHandler) GetLinuxDOAuthorize(c *gin.Context) {
	h.startLinuxDO(c, linuxDOIntentLogin)
}

func (h *IAMHandler) GetLinuxDOBind(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		redirectLinuxDOError(c, linuxDOIntentLogin, "session")
		return
	}
	bound, err := h.module.LoginUseCase.HasLinuxDOIdentity(c.Request.Context(), userID)
	if err != nil {
		slog.Error("check linuxdo binding", "request_id", middleware.GetRequestID(c), "error", err)
		redirectLinuxDOError(c, linuxDOIntentBind, "failed")
		return
	}
	if bound {
		redirectLinuxDONotice(c, "linuxdo_bound")
		return
	}
	h.startLinuxDO(c, linuxDOIntentBind)
}

func (h *IAMHandler) startLinuxDO(c *gin.Context, intent string) {
	c.Header("Cache-Control", "no-store")
	settings, enabled := currentLinuxDOSettings()
	if !enabled || h.module == nil || h.module.SessionStore == nil {
		redirectLinuxDOError(c, intent, "disabled")
		return
	}
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.HitOAuthStart(c.Request.Context(), linuxDOProvider, c.ClientIP())
		if err != nil {
			slog.Error("linuxdo oauth start abuse limit", "request_id", middleware.GetRequestID(c), "error", err)
			redirectLinuxDOError(c, intent, "failed")
			return
		}
		if retryAfter > 0 {
			redirectLinuxDOError(c, intent, "rate_limited")
			return
		}
	}

	state, err := newCSRFToken()
	if err != nil {
		slog.Error("generate linuxdo oauth state", "request_id", middleware.GetRequestID(c), "error", err)
		redirectLinuxDOError(c, intent, "failed")
		return
	}
	codeVerifier, err := newCSRFToken()
	if err != nil {
		slog.Error("generate linuxdo pkce verifier", "request_id", middleware.GetRequestID(c), "error", err)
		redirectLinuxDOError(c, intent, "failed")
		return
	}
	flow := app.OAuthFlow{Provider: linuxDOProvider, Intent: intent, CodeVerifier: codeVerifier}
	if intent == linuxDOIntentBind {
		userID, userOK := middleware.GetCurrentUserID(c)
		sessionID, sessionOK := middleware.GetCurrentSessionID(c)
		if !userOK || !sessionOK {
			redirectLinuxDOError(c, linuxDOIntentLogin, "session")
			return
		}
		flow.UserID = userID
		flow.SessionID = sessionID
	}
	if err := h.module.SessionStore.PutOAuthFlow(c.Request.Context(), state, flow, linuxDOFlowMaxAge); err != nil {
		slog.Error("store linuxdo oauth flow", "request_id", middleware.GetRequestID(c), "error", err)
		redirectLinuxDOError(c, intent, "failed")
		return
	}

	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(linuxDOStateCookieName, state, int(linuxDOFlowMaxAge.Seconds()), linuxDOCallbackPath, "", h.sessionSecure, true)

	authorizeURL, _ := url.Parse(linuxDOAuthorizeEndpoint)
	challenge := sha256.Sum256([]byte(codeVerifier))
	query := authorizeURL.Query()
	query.Set("client_id", settings.ClientID)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	query.Set("redirect_uri", settings.CallbackURL)
	query.Set("response_type", "code")
	scope := "openid profile"
	if intent == linuxDOIntentLogin {
		scope += " email"
	}
	query.Set("scope", scope)
	query.Set("state", state)
	authorizeURL.RawQuery = query.Encode()
	c.Redirect(http.StatusFound, authorizeURL.String())
}

func (h *IAMHandler) GetLinuxDOCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	state := strings.TrimSpace(c.Query("state"))
	cookieState, cookieErr := c.Cookie(linuxDOStateCookieName)
	h.clearLinuxDOFlowCookies(c)
	if cookieErr != nil || state == "" || len(state) > 128 || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		redirectLinuxDOError(c, linuxDOIntentLogin, "state")
		return
	}
	if h.module == nil || h.module.SessionStore == nil {
		redirectLinuxDOError(c, linuxDOIntentLogin, "failed")
		return
	}
	flow, err := h.module.SessionStore.ConsumeOAuthFlow(c.Request.Context(), state)
	if err != nil {
		slog.Error("consume linuxdo oauth flow", "request_id", middleware.GetRequestID(c), "error", err)
		redirectLinuxDOError(c, linuxDOIntentLogin, "failed")
		return
	}
	if flow == nil || flow.Provider != linuxDOProvider || flow.Intent != linuxDOIntentLogin && flow.Intent != linuxDOIntentBind {
		redirectLinuxDOError(c, linuxDOIntentLogin, "state")
		return
	}
	intent := flow.Intent
	if intent == linuxDOIntentBind {
		userID, userOK := middleware.GetCurrentUserID(c)
		sessionID, sessionOK := middleware.GetCurrentSessionID(c)
		if !userOK || !sessionOK || userID != flow.UserID || subtle.ConstantTimeCompare([]byte(sessionID), []byte(flow.SessionID)) != 1 {
			redirectLinuxDOError(c, linuxDOIntentLogin, "session")
			return
		}
	}

	if strings.TrimSpace(c.Query("error")) != "" {
		redirectLinuxDOError(c, intent, "cancelled")
		return
	}

	settings, enabled := currentLinuxDOSettings()
	if !enabled || h.module == nil || h.module.LoginUseCase == nil || h.module.linuxDOClient == nil {
		redirectLinuxDOError(c, intent, "disabled")
		return
	}
	if h.module.AbuseLimiter != nil {
		retryAfter, err := h.module.AbuseLimiter.HitOAuth(c.Request.Context(), linuxDOProvider, c.ClientIP())
		if err != nil {
			slog.Error("linuxdo oauth abuse limit", "request_id", middleware.GetRequestID(c), "error", err)
			redirectLinuxDOError(c, intent, "failed")
			return
		}
		if retryAfter > 0 {
			redirectLinuxDOError(c, intent, "rate_limited")
			return
		}
	}

	profile, err := h.module.linuxDOClient.Exchange(c.Request.Context(), strings.TrimSpace(c.Query("code")), flow.CodeVerifier, settings)
	if err != nil {
		slog.Warn("linuxdo oauth provider request failed", "request_id", middleware.GetRequestID(c), "error", err)
		redirectLinuxDOError(c, intent, "failed")
		return
	}

	if intent == linuxDOIntentBind {
		userID, _ := middleware.GetCurrentUserID(c)
		if err := h.module.LoginUseCase.BindLinuxDO(c.Request.Context(), userID, profile); err != nil {
			switch {
			case errors.Is(err, domain.ErrLinuxDOIdentityAlreadyBound):
				redirectLinuxDOError(c, intent, "already_bound")
			case errors.Is(err, domain.ErrLinuxDOTrustLevelTooLow):
				redirectLinuxDOError(c, intent, "trust_level")
			case errors.Is(err, domain.ErrLinuxDOAccountUnavailable):
				redirectLinuxDOError(c, intent, "account")
			default:
				slog.Error("bind linuxdo oauth identity", "request_id", middleware.GetRequestID(c), "error", err)
				redirectLinuxDOError(c, intent, "failed")
			}
			return
		}
		redirectLinuxDONotice(c, "linuxdo_bound")
		return
	}

	csrfToken, err := newCSRFToken()
	if err != nil {
		slog.Error("generate linuxdo csrf token", "request_id", middleware.GetRequestID(c), "error", err)
		redirectLinuxDOError(c, intent, "failed")
		return
	}
	result, pending, err := h.module.LoginUseCase.LoginLinuxDO(c.Request.Context(), profile, app.LoginMeta{
		ClientIP:  c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrRegistrationDisabled):
			redirectLinuxDOError(c, intent, "registration_disabled")
		case errors.Is(err, domain.ErrLinuxDOTrustLevelTooLow):
			redirectLinuxDOError(c, intent, "trust_level")
		case errors.Is(err, domain.ErrLinuxDOAccountUnavailable):
			redirectLinuxDOError(c, intent, "account")
		default:
			slog.Error("linuxdo oauth login failed", "request_id", middleware.GetRequestID(c), "error", err)
			redirectLinuxDOError(c, intent, "failed")
		}
		return
	}
	if pending != nil {
		if h.module.LinuxDOPendingStore == nil {
			redirectLinuxDOError(c, intent, "failed")
			return
		}
		token, err := newCSRFToken()
		if err != nil {
			slog.Error("generate linuxdo pending token", "request_id", middleware.GetRequestID(c), "error", err)
			redirectLinuxDOError(c, intent, "failed")
			return
		}
		if err := h.module.LinuxDOPendingStore.PutLinuxDOPending(c.Request.Context(), token, *pending, linuxDOFlowMaxAge); err != nil {
			slog.Error("store linuxdo pending setup", "request_id", middleware.GetRequestID(c), "error", err)
			redirectLinuxDOError(c, intent, "failed")
			return
		}
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(linuxDOPendingCookieName, token, int(linuxDOFlowMaxAge.Seconds()), linuxDOPendingPath, "", h.sessionSecure, true)
		c.Redirect(http.StatusSeeOther, "/login?oauth_setup=linuxdo")
		return
	}
	if result == nil {
		redirectLinuxDOError(c, intent, "failed")
		return
	}
	setAuthCookies(c, result.Session.ID, csrfToken, result.SessionMaxAge, h.sessionSecure)
	c.Redirect(http.StatusSeeOther, "/login")
}

func (h *IAMHandler) GetLinuxDOPending(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_, pending, err := h.linuxDOPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, LinuxDOPendingResponse{
		Provider:             linuxDOProvider,
		ProviderUserID:       pending.Profile.ID,
		Username:             linuxDOProfileName(pending.Profile),
		SuggestedEmail:       pending.SuggestedEmail,
		SuggestedEmailExists: pending.SuggestedEmailExists,
		RegistrationEnabled:  pending.LegacyUserID != 0 || runtimeconfig.Bool("register_enabled", true),
		LegacyAccount:        pending.LegacyUserID != 0,
	})
}

func (h *IAMHandler) PostLinuxDOEmailCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_, pending, err := h.linuxDOPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	var req LinuxDOEmailCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if !h.verifyTurnstile(c, req.TurnstileToken, turnstileActionLinuxDOEmail) {
		return
	}
	created, err := h.module.EmailCodeUseCase.RequestLinuxDO(c.Request.Context(), req.Email, pending.Profile.Email, req.Mode, pending.LegacyUserID != 0)
	if err != nil {
		writeError(c, err)
		return
	}
	if created && h.module.AbuseLimiter != nil {
		if err := h.module.AbuseLimiter.ClearEmailCodeFailures(c.Request.Context(), req.Email); err != nil {
			slog.Warn("clear linuxdo email code abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	c.Header("Retry-After", strconv.Itoa(app.EmailCodeResendGapSeconds()))
	c.Status(http.StatusNoContent)
}

func (h *IAMHandler) PostLinuxDOComplete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token, pending, err := h.linuxDOPending(c)
	if err != nil {
		writeError(c, err)
		return
	}
	var req LinuxDOCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
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
	result, err := h.module.LoginUseCase.CompleteLinuxDO(c.Request.Context(), *pending, req.Mode, req.Email, req.Code, app.LoginMeta{
		ClientIP:  clientIP,
		UserAgent: c.Request.UserAgent(),
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
			slog.Warn("clear linuxdo setup abuse limit", "request_id", middleware.GetRequestID(c), "error", err.Error())
		}
	}
	if err := h.module.LinuxDOPendingStore.DeleteLinuxDOPending(c.Request.Context(), token); err != nil {
		slog.Warn("delete linuxdo pending setup", "request_id", middleware.GetRequestID(c), "error", err.Error())
	}
	h.clearLinuxDOPendingCookie(c)
	setAuthCookies(c, result.Session.ID, csrfToken, result.SessionMaxAge, h.sessionSecure)
	c.JSON(http.StatusOK, LoginResponse{User: h.userResponseWithPermissions(c.Request.Context(), result.User)})
}

func (h *IAMHandler) linuxDOPending(c *gin.Context) (string, *app.LinuxDOPending, error) {
	if h.module == nil || h.module.LinuxDOPendingStore == nil {
		return "", nil, domain.ErrLinuxDOPendingExpired
	}
	token, err := c.Cookie(linuxDOPendingCookieName)
	token = strings.TrimSpace(token)
	if err != nil || token == "" || len(token) > 128 {
		h.clearLinuxDOPendingCookie(c)
		return "", nil, domain.ErrLinuxDOPendingExpired
	}
	pending, err := h.module.LinuxDOPendingStore.GetLinuxDOPending(c.Request.Context(), token)
	if err != nil {
		return "", nil, fmt.Errorf("get linuxdo pending setup: %w", err)
	}
	if pending == nil {
		h.clearLinuxDOPendingCookie(c)
		return "", nil, domain.ErrLinuxDOPendingExpired
	}
	return token, pending, nil
}

func (h *IAMHandler) clearLinuxDOPendingCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(linuxDOPendingCookieName, "", -1, linuxDOPendingPath, "", h.sessionSecure, true)
}

func linuxDOProfileName(profile app.LinuxDOProfile) string {
	if name := strings.TrimSpace(profile.Username); name != "" {
		return name
	}
	if name := strings.TrimSpace(profile.Name); name != "" {
		return name
	}
	return "LinuxDO User"
}

func (h *IAMHandler) clearLinuxDOFlowCookies(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(linuxDOStateCookieName, "", -1, linuxDOCallbackPath, "", h.sessionSecure, true)
}

func redirectLinuxDOError(c *gin.Context, intent, code string) {
	path := "/login"
	if intent == linuxDOIntentBind {
		path = "/account"
	}
	c.Redirect(http.StatusSeeOther, path+"?oauth_error="+url.QueryEscape(code))
}

func redirectLinuxDONotice(c *gin.Context, code string) {
	c.Redirect(http.StatusSeeOther, "/account?oauth_notice="+url.QueryEscape(code))
}

type linuxDOClient struct {
	httpClient *http.Client
	tokenURL   string
	userURL    string
}

func newLinuxDOClient() *linuxDOClient {
	return &linuxDOClient{
		httpClient: &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		tokenURL: linuxDOTokenEndpoint,
		userURL:  linuxDOUserEndpoint,
	}
}

func (client *linuxDOClient) Exchange(ctx context.Context, code, codeVerifier string, settings linuxDOSettings) (app.LinuxDOProfile, error) {
	if client == nil || client.httpClient == nil || code == "" || len(code) > 2048 || len(codeVerifier) < 43 || len(codeVerifier) > 128 {
		return app.LinuxDOProfile{}, errors.New("invalid linuxdo authorization code")
	}

	form := url.Values{
		"client_id":     {settings.ClientID},
		"client_secret": {settings.ClientSecret},
		"code":          {code},
		"code_verifier": {codeVerifier},
		"redirect_uri":  {settings.CallbackURL},
		"grant_type":    {"authorization_code"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return app.LinuxDOProfile{}, fmt.Errorf("create linuxdo token request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return app.LinuxDOProfile{}, fmt.Errorf("request linuxdo token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return app.LinuxDOProfile{}, fmt.Errorf("linuxdo token status %d", response.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, linuxDOMaxResponseBytes)).Decode(&token); err != nil {
		return app.LinuxDOProfile{}, fmt.Errorf("decode linuxdo token: %w", err)
	}
	token.AccessToken = strings.TrimSpace(token.AccessToken)
	if token.AccessToken == "" || len(token.AccessToken) > 8192 || strings.ContainsAny(token.AccessToken, "\r\n\x00") {
		return app.LinuxDOProfile{}, errors.New("linuxdo token response is invalid")
	}

	request, err = http.NewRequestWithContext(ctx, http.MethodGet, client.userURL, nil)
	if err != nil {
		return app.LinuxDOProfile{}, fmt.Errorf("create linuxdo user request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err = client.httpClient.Do(request)
	if err != nil {
		return app.LinuxDOProfile{}, fmt.Errorf("request linuxdo user: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return app.LinuxDOProfile{}, fmt.Errorf("linuxdo user status %d", response.StatusCode)
	}
	var user struct {
		ID         json.RawMessage `json:"id"`
		Sub        string          `json:"sub"`
		Username   string          `json:"username"`
		Name       string          `json:"name"`
		Email      string          `json:"email"`
		Active     bool            `json:"active"`
		Silenced   bool            `json:"silenced"`
		TrustLevel int             `json:"trust_level"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, linuxDOMaxResponseBytes)).Decode(&user); err != nil {
		return app.LinuxDOProfile{}, fmt.Errorf("decode linuxdo user: %w", err)
	}
	linuxDOID, err := parseLinuxDOUserID(user.ID, user.Sub)
	if err != nil {
		return app.LinuxDOProfile{}, errors.New("linuxdo user id is empty")
	}
	return app.LinuxDOProfile{
		ID:         linuxDOID,
		Username:   user.Username,
		Name:       user.Name,
		Email:      user.Email,
		Active:     user.Active,
		Silenced:   user.Silenced,
		TrustLevel: user.TrustLevel,
	}, nil
}

func parseLinuxDOUserID(raw json.RawMessage, sub string) (string, error) {
	value := ""
	if len(raw) > 0 && string(raw) != "null" {
		var numeric uint64
		if err := json.Unmarshal(raw, &numeric); err == nil && numeric > 0 {
			value = strconv.FormatUint(numeric, 10)
		} else {
			var textID string
			if err := json.Unmarshal(raw, &textID); err == nil {
				value = strings.TrimSpace(textID)
			}
		}
	}
	if value == "" {
		value = strings.TrimSpace(sub)
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != value {
		return "", domain.ErrLinuxDOAccountUnavailable
	}
	return value, nil
}
