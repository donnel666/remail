package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAuthRequiredClearsInvalidSessionCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(LoadSession(SessionFetcherFunc(func(context.Context, string) (uint, domain.RoleLevel, string, bool) {
		return 0, domain.RoleUser, "", false
	})))
	router.Use(AuthRequired())
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "expired-session"})
	request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "stale-csrf"})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusUnauthorized, response.Code)
	cookies := response.Result().Cookies()
	requireCookieCleared(t, cookies, SessionCookieName)
	requireCookieCleared(t, cookies, CSRFCookieName)
}

func requireCookieCleared(t *testing.T, cookies []*http.Cookie, name string) {
	t.Helper()

	for _, cookie := range cookies {
		if cookie.Name == name {
			require.Empty(t, cookie.Value)
			require.Negative(t, cookie.MaxAge)
			return
		}
	}
	require.Failf(t, "cookie was not cleared", "missing cookie %s", name)
}
