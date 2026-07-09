package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

type noSessionFetcher struct{}

func (noSessionFetcher) FetchSession(context.Context, string) (uint, iamdomain.Role, string, bool) {
	return 0, "", "", false
}

type noPermissionChecker struct{}

func (noPermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return false, nil
}

func TestConsoleOrderRoutesIgnoreAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	RegisterRoutes(r.Group("/v1"), nil, noSessionFetcher{}, noPermissionChecker{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/orders", nil)
	req.Header.Set("Authorization", "Bearer rk-test")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}
}
