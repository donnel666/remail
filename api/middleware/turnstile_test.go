package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubTurnstileVerifier struct {
	action string
	token  string
	err    error
	calls  int
}

func (s *stubTurnstileVerifier) Verify(_ context.Context, token, _, expectedAction string) error {
	s.calls++
	s.token = token
	s.action = expectedAction
	return s.err
}

func turnstileRouter(verifier TurnstileVerifier) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/v1")
	v1.Use(func(c *gin.Context) {
		if sid, err := c.Cookie(SessionCookieName); err == nil && sid == "session-id" {
			SetCurrentUser(c, 7, iamdomain.RoleUser, "", sid)
		}
		c.Next()
	})
	v1.Use(AuthRequired())
	v1.Use(CSRFRequired())
	v1.Use(TurnstileGuard(verifier, nil))
	v1.GET("/tickets", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	v1.POST("/tickets", func(c *gin.Context) { c.Status(http.StatusCreated) })
	v1.POST("/orders", func(c *gin.Context) { c.Status(http.StatusCreated) })
	v1.POST("/open/cards/redeem", func(c *gin.Context) { c.Status(http.StatusOK) })
	v1.POST("/projects/:projectId/resubmit", func(c *gin.Context) { c.Status(http.StatusOK) })
	v1.POST("/domains", func(c *gin.Context) { c.Status(http.StatusCreated) })
	return router
}

// doTurnstile sends a request that has passed the authentication and CSRF
// middleware which precede the guard in production.
func doTurnstile(router *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	return doTurnstileRaw(router, method, path, token, "session-id")
}

func doTurnstileRaw(router *gin.Engine, method, path, token, sid string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.Header.Set(TurnstileHeaderName, token)
	}
	if sid != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sid})
	}
	request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf"})
	request.Header.Set(CSRFHeaderName, "csrf")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestTurnstileGuardRunsAfterAuthenticationAndCSRF(t *testing.T) {
	verifier := &stubTurnstileVerifier{}
	router := turnstileRouter(verifier)

	require.Equal(t, http.StatusUnauthorized, doTurnstileRaw(router, http.MethodPost, "/v1/domains", "tok", "").Code)
	require.Equal(t, http.StatusUnauthorized, doTurnstileRaw(router, http.MethodPost, "/v1/domains", "tok", "forged-session").Code)
	require.Zero(t, verifier.calls)

	request := httptest.NewRequest(http.MethodPost, "/v1/domains", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session-id"})
	request.Header.Set(TurnstileHeaderName, "tok")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Zero(t, verifier.calls)

	require.Equal(t,
		http.StatusUnprocessableEntity,
		doTurnstileRaw(router, http.MethodPost, "/v1/domains", "", "session-id").Code)
}

func TestTurnstileGuardGuardsListedWritesOnly(t *testing.T) {
	verifier := &stubTurnstileVerifier{}
	router := turnstileRouter(verifier)

	// Guarded write without a token is rejected before the handler runs.
	require.Equal(t, http.StatusUnprocessableEntity, doTurnstile(router, http.MethodPost, "/v1/tickets", "").Code)
	require.Zero(t, verifier.calls, "no token must not cost a siteverify call")

	// Same path, safe method: the list endpoint stays open.
	require.Equal(t, http.StatusNoContent, doTurnstile(router, http.MethodGet, "/v1/tickets", "").Code)

	// Unlisted write and the API-key surface pass straight through.
	require.Equal(t, http.StatusCreated, doTurnstile(router, http.MethodPost, "/v1/orders", "").Code)
	require.Equal(t, http.StatusOK, doTurnstile(router, http.MethodPost, "/v1/open/cards/redeem", "").Code)
	require.Zero(t, verifier.calls)
}

func TestTurnstileGuardVerifiesRouteAction(t *testing.T) {
	verifier := &stubTurnstileVerifier{}
	router := turnstileRouter(verifier)

	require.Equal(t, http.StatusCreated, doTurnstile(router, http.MethodPost, "/v1/tickets", "tok").Code)
	require.Equal(t, "ticket_create", verifier.action)
	require.Equal(t, "tok", verifier.token)

	// A parameterised route resolves to its own action, not the literal path.
	require.Equal(t, http.StatusOK, doTurnstile(router, http.MethodPost, "/v1/projects/7/resubmit", "tok").Code)
	require.Equal(t, "project_resubmit", verifier.action)
}

func TestTurnstileGuardRejectsBadTokenAndUnavailableService(t *testing.T) {
	invalid := &stubTurnstileVerifier{err: iamdomain.ErrTurnstileInvalid}
	require.Equal(t,
		http.StatusUnprocessableEntity,
		doTurnstile(turnstileRouter(invalid), http.MethodPost, "/v1/tickets", "tok").Code)

	down := &stubTurnstileVerifier{err: errors.New("dial tcp: timeout")}
	require.Equal(t,
		http.StatusServiceUnavailable,
		doTurnstile(turnstileRouter(down), http.MethodPost, "/v1/tickets", "tok").Code)

	// A nil verifier must fail closed, not open.
	require.Equal(t,
		http.StatusServiceUnavailable,
		doTurnstile(turnstileRouter(nil), http.MethodPost, "/v1/tickets", "tok").Code)
}

func TestTurnstileGuardSkippedWhenCaptchaDisabled(t *testing.T) {
	runtimeconfig.Replace([]settingsdomain.Setting{{Key: "captcha_enabled", Value: "false"}})
	t.Cleanup(func() { runtimeconfig.Replace(nil) })

	verifier := &stubTurnstileVerifier{}
	require.Equal(t,
		http.StatusCreated,
		doTurnstile(turnstileRouter(verifier), http.MethodPost, "/v1/tickets", "").Code)
	require.Zero(t, verifier.calls)
}

func TestTurnstileActionsCoverDistinctActions(t *testing.T) {
	seen := make(map[string]string, len(turnstileActions))
	for route, action := range turnstileActions {
		require.NotEmpty(t, action, route)
		if other, dup := seen[action]; dup {
			t.Fatalf("action %q is shared by %s and %s; a token minted for one route would replay against the other", action, other, route)
		}
		seen[action] = route
	}
}
