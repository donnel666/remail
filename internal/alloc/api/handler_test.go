package api

import (
	"context"
	"encoding/json"
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
	seedAdminAllocationReadComposition(t, db)

	t.Run("unauthenticated", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/allocations", false)
		require.Equal(t, http.StatusUnauthorized, resp.Code)
	})

	t.Run("non admin forbidden", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleUser}, fakePermissionChecker{allowed: false})
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

	t.Run("enriched allocation list matches openapi", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/allocations?type=microsoft&resourceId=1000&limit=20", true)
		require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
		var payload map[string]any
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &payload))
		items, ok := payload["items"].([]any)
		require.True(t, ok)
		require.Len(t, items, 1)
		item := items[0].(map[string]any)
		for _, key := range []string{
			"type", "id", "orderNo", "projectId", "projectName", "projectLogoUrl", "resourceId", "mailbox",
			"supplyScope", "deliveryEmail", "serviceMode", "orderStatus", "status", "payAmount", "buyerEmail",
			"verificationCode", "createdAt", "receiveUntil",
		} {
			require.Contains(t, item, key)
		}
		require.Equal(t, "Alloc API Project", item["projectName"])
		require.Equal(t, "/v1/projects/logos/alloc-api", item["projectLogoUrl"])
		require.Equal(t, "buyer-mailbox@example.com", item["deliveryEmail"])
		require.Equal(t, "admin@test.local", item["buyerEmail"])
		require.Equal(t, "654321", item["verificationCode"])
		require.NotContains(t, item, "productId")
		require.NotContains(t, item, "email")
		require.NotContains(t, item, "releasedAt")
	})

	t.Run("allocation list rejects limit above contract", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/allocations?limit=101", true)
		require.Equal(t, http.StatusBadRequest, resp.Code)
	})

	t.Run("inventory rejects unavailable project", func(t *testing.T) {
		router := newAllocationAPITestRouter(NewModule(db, nil), fakeSessionFetcher{ok: true, role: iamdomain.RoleAdmin}, fakePermissionChecker{allowed: true})
		resp := performAllocAPIRequest(router, http.MethodGet, "/v1/admin/projects/999/inventory", true)
		require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
	})

}

type fakeSessionFetcher struct {
	ok   bool
	role iamdomain.Role
}

func (f fakeSessionFetcher) FetchSession(context.Context, string) (uint, iamdomain.Role, string, bool) {
	if !f.ok {
		return 0, "", "", false
	}
	role := f.role
	if role == "" {
		role = iamdomain.RoleAdmin
	}
	return 1, role, "admin@test.local", true
}

type fakePermissionChecker struct {
	allowed bool
	err     error
}

func (f fakePermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
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
	INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
	    (1, 'admin@test.local', 'hash', 'admin', TRUE, 'admin')`).Error)
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

func seedAdminAllocationReadComposition(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("UPDATE projects SET logo_url = '/v1/projects/logos/alloc-api' WHERE id = 10").Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES (1000, 'microsoft', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, email_address, email_domain, password, status, for_sale, quality_score, alloc_bucket)
VALUES (1000, 'admin-allocation@outlook.com', 'outlook.com', 'write-only', 'normal', FALSE, 100, MOD(1000, 64))`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO orders(
    id, order_no, user_id, project_id, project_product_id, product_type, service_mode,
    supply_policy, status, pay_amount, delivery_email, receive_until, client_channel,
    idempotency_key, request_fingerprint
) VALUES (
    2000, 'ORD-ADMIN-ALLOC', 1, 10, 20, 'microsoft', 'code',
    'private_first', 'pending_payment', 12.34, 'buyer-mailbox@example.com',
    DATE_ADD(UTC_TIMESTAMP(), INTERVAL 10 MINUTE), 'console', 'admin-alloc-idem',
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type) VALUES ('ORD-ADMIN-ALLOC', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    id, order_no, project_id, product_id, resource_id, supply_scope, mailbox, email, status
) VALUES (
    3000, 'ORD-ADMIN-ALLOC', 10, 20, 1000, 'owned', 'main', 'admin-allocation@outlook.com', 'allocated'
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_messages(
    id, email_resource_id, resource_type, matched_order_id, recipient, sender, subject,
    body_preview, verification_code, dedupe_key, status, received_at
) VALUES (
    4000, 1000, 'microsoft', 2000, 'admin-allocation@outlook.com', 'sender@example.com',
    'Verification', 'Your code is 654321', '654321',
    'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'matched', UTC_TIMESTAMP()
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_order_delivery_heads(order_id, message_id, message_received_at)
VALUES (2000, 4000, UTC_TIMESTAMP(3))`).Error)
}
