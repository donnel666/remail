package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var allocAPIMySQLTestServer = testmysql.New("remail_alloc_api_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = allocAPIMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newAllocAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return allocAPIMySQLTestServer.Database(t, allocAPIMigrationsDir(t))
}

func allocAPIMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestAllocationAdminRoutesAuthAndContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAllocAPITestDB(t)
	seedAllocationAPITestProject(t, db)

	t.Run("unauthenticated", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/allocations", false)
		require.Equal(t, http.StatusUnauthorized, resp.Code)
	})

	t.Run("non admin forbidden", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleUser}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/allocations", true)
		require.Equal(t, http.StatusForbidden, resp.Code)
	})

	t.Run("permission denied", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: false})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/allocations", true)
		require.Equal(t, http.StatusForbidden, resp.Code)
	})

	t.Run("user inventory exposes only total", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleUser}, fakePermissionChecker{allowed: false})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/projects/10/inventory", true)
		require.Equal(t, http.StatusOK, resp.Code)
		require.Contains(t, resp.Body.String(), `"totalAvailable"`)
		require.Contains(t, resp.Body.String(), `"products"`)
		require.Contains(t, resp.Body.String(), `"productId"`)
		require.NotContains(t, resp.Body.String(), `"microsoft"`)
		require.NotContains(t, resp.Body.String(), `"domain"`)
	})

	t.Run("invalid filter", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/allocations?type=invalid", true)
		require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
	})

	t.Run("candidate refresh accepted", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodPost, "/v1/admin/projects/10/candidates/refresh", true)
		require.Equal(t, http.StatusAccepted, resp.Code)
		require.Contains(t, resp.Body.String(), `"status"`)
		require.Contains(t, resp.Body.String(), `"jobId"`)
	})

	t.Run("domain candidate type is accepted", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/projects/10/candidates?type=domain", true)
		require.Equal(t, http.StatusOK, resp.Code)
	})

	t.Run("inventory rejects unavailable project", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/projects/999/inventory", true)
		require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
	})

	t.Run("invalid candidate type is rejected", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/projects/10/candidates?type=invalid", true)
		require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
	})
}

type fakeSessionFetcher struct {
	ok   bool
	role iamdomain.RoleLevel
}

func (f fakeSessionFetcher) FetchSession(context.Context, string) (uint, iamdomain.RoleLevel, string, bool) {
	if !f.ok {
		return 0, 0, "", false
	}
	role := f.role
	if role == 0 {
		role = iamdomain.RoleAdmin
	}
	return 1, role, "admin@test.local", true
}

type fakePermissionChecker struct {
	allowed bool
	err     error
}

func (f fakePermissionChecker) Check(context.Context, uint, iamdomain.RoleLevel, string, string) (bool, error) {
	return f.allowed, f.err
}

func newAllocationAPITestRouter(mod *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) *gin.Engine {
	router := gin.New()
	router.Use(middleware.RequestID())
	v1 := router.Group("/v1")
	RegisterRoutes(v1, mod, fetcher, checker)
	return router
}

func performAllocAPIRequest(router *gin.Engine, method string, path string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authenticated {
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	}
	if method != http.MethodGet {
		req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
		req.Header.Set(middleware.CSRFHeaderName, "csrf")
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func seedAllocationAPITestProject(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
	INSERT INTO users(id, email, password_hash, nickname, enabled, role_level) VALUES
	    (1, 'admin@test.local', 'hash', 'admin', TRUE, 80)`).Error)
	require.NoError(t, db.Exec(`
	INSERT INTO projects(id, name, target_platform, status, access_type)
	VALUES (10, 'Alloc API Project', 'alloc', 'listed', 'public')`).Error)
	require.NoError(t, db.Exec(`
	INSERT INTO project_products(
	    id, project_id, type, status, code_enabled, purchase_enabled,
	    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
	    code_window_minutes, activation_window_minutes, warranty_minutes,
	    main_weight, dot_weight, plus_weight
	) VALUES (20, 10, 'microsoft', 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 1, 0, 0)`).Error)
}
