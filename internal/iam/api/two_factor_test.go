package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type apiTwoFactorStub struct{ app.TwoFactorService }

func (apiTwoFactorStub) Enabled(context.Context, uint) (bool, error) { return true, nil }
func (apiTwoFactorStub) Setup(context.Context, uint, string, time.Duration) (string, string, error) {
	return "otpauth://totp/Remail:test?secret=JBSWY3DPEHPK3PXP", "setup-token", nil
}
func (apiTwoFactorStub) Enable(context.Context, uint, string, string) error { return nil }
func (apiTwoFactorStub) Disable(context.Context, uint, string) error        { return nil }
func (apiTwoFactorStub) ForceDisable(context.Context, uint, *governancedomain.OperationLog) error {
	return nil
}
func (apiTwoFactorStub) EnabledForUsers(context.Context, []uint) (map[uint]bool, error) {
	return map[uint]bool{}, nil
}
func (apiTwoFactorStub) BeginLogin(context.Context, string, domain.LoginChallenge, time.Duration) error {
	return nil
}

func TestPostLoginTwoFactorOnlySetsPendingCookie(t *testing.T) {
	h := newTestHandler()
	service := apiTwoFactorStub{}
	h.module.LoginUseCase.SetTwoFactorService(service)
	r := setupTestRouterWithHandler(h)
	seedUser(t, h, "user@test.com")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"email":"user@test.com","password":"User123!","turnstileToken":"valid-turnstile"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.JSONEq(t, `{"twoFactorRequired":true}`, w.Body.String())
	found := false
	for _, cookie := range w.Result().Cookies() {
		require.NotEqual(t, middleware.SessionCookieName, cookie.Name)
		if cookie.Name == twoFactorLoginCookie {
			found = true
			require.True(t, cookie.HttpOnly)
			require.Equal(t, "/v1/login/2fa", cookie.Path)
		}
	}
	require.True(t, found)
}

func TestLogoutClearsTwoFactorPendingCookies(t *testing.T) {
	h := newTestHandler()
	r := setupTestRouterWithHandler(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/sessions/current", nil))
	require.Equal(t, http.StatusNoContent, w.Code)
	cleared := map[string]string{}
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = cookie.Path
		}
	}
	require.Equal(t, "/v1/login/2fa", cleared[twoFactorLoginCookie])
	require.Equal(t, "/v1/me/2fa", cleared[twoFactorSetupCookie])
}

func TestFinalLoginClearsPendingTwoFactorCookies(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setAuthCookies(c, "session", "csrf", 300, false)
	cleared := map[string]string{}
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = cookie.Path
		}
	}
	require.Equal(t, "/v1/login/2fa", cleared[twoFactorLoginCookie])
	require.Equal(t, "/v1/me/2fa", cleared[twoFactorSetupCookie])
}

func TestTwoFactorEnableReturnsCommittedStateWhenSessionCleanupFails(t *testing.T) {
	h := newTestHandler()
	h.module.TwoFactorService = apiTwoFactorStub{}
	h.module.SessionStore.(*mockSessionStore).deleteByUserIDErr = errors.New("redis unavailable")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	middleware.SetCurrentUser(c, 7, domain.RoleUser, "user@example.test", "session")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/me/2fa/enable", strings.NewReader(`{"code":"123456"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.AddCookie(&http.Cookie{Name: twoFactorSetupCookie, Value: "setup-token"})
	h.PostTwoFactorEnable(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"enabled":true}`, w.Body.String())
}

type resetTwoFactorStub struct {
	app.TwoFactorService
	target uint
	audit  *governancedomain.OperationLog
}

func (s *resetTwoFactorStub) ForceDisable(_ context.Context, target uint, audit *governancedomain.OperationLog) error {
	s.target, s.audit = target, audit
	return nil
}

func TestAdminTwoFactorRecoveryRequiresSuperAdminAndRecordsOperator(t *testing.T) {
	for _, role := range []domain.Role{domain.RoleUser, domain.RoleAdmin, domain.RoleSuperAdmin} {
		t.Run(string(role), func(t *testing.T) {
			h := newTestHandler()
			svc := &resetTwoFactorStub{}
			h.module.TwoFactorService = svc
			r := gin.New()
			r.DELETE("/v1/admin/users/:userId/2fa", func(c *gin.Context) {
				middleware.SetCurrentUser(c, 1, role, "operator@example.test", "session")
				h.DeleteAdminTwoFactor(c)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/admin/users/7/2fa", nil))
			if role != domain.RoleSuperAdmin {
				require.Equal(t, http.StatusForbidden, w.Code)
				require.Zero(t, svc.target)
				return
			}
			require.Equal(t, http.StatusNoContent, w.Code)
			require.EqualValues(t, 7, svc.target)
			require.NotNil(t, svc.audit)
			require.EqualValues(t, 1, svc.audit.OperatorUserID)
			require.Equal(t, "7", svc.audit.ResourceID)
		})
	}
}
