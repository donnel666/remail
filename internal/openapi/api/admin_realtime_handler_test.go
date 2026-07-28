package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminRealtimeSessionFetcher struct{}

func (adminRealtimeSessionFetcher) FetchSession(_ context.Context, sessionID string) (uint, iamdomain.Role, string, bool) {
	return 1, iamdomain.RoleAdmin, "admin@example.com", sessionID == "valid"
}

type adminRealtimePermissionChecker map[string]bool

func (c adminRealtimePermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	return c[resource+":"+action], nil
}

func TestAdminUserRealtimeUsageRouteAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	allowed := adminRealtimePermissionChecker{"iam:user:read": true}

	t.Run("requires authentication", func(t *testing.T) {
		response := adminRealtimeRequest(t, "/v1/admin/users/42/apikeys/realtime-usage", allowed, false)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	})

	t.Run("requires user read", func(t *testing.T) {
		response := adminRealtimeRequest(t, "/v1/admin/users/42/apikeys/realtime-usage", adminRealtimePermissionChecker{}, true)
		require.Equal(t, http.StatusForbidden, response.Code)
	})

	t.Run("rejects invalid user id", func(t *testing.T) {
		response := adminRealtimeRequest(t, "/v1/admin/users/invalid/apikeys/realtime-usage", allowed, true)
		require.Equal(t, http.StatusBadRequest, response.Code)
	})

	t.Run("returns selected user load", func(t *testing.T) {
		response := adminRealtimeRequest(t, "/v1/admin/users/42/apikeys/realtime-usage", allowed, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.JSONEq(t, `{"activeRequests":0,"requestsPerMinute":0}`, response.Body.String())
	})
}

func adminRealtimeRequest(t *testing.T, target string, checker middleware.PermissionChecker, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	module := NewModule(nil)
	t.Cleanup(func() { require.NoError(t, module.UseCase.Close(context.Background())) })
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterRoutes(router.Group("/v1"), module, adminRealtimeSessionFetcher{}, checker)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if authenticated {
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
