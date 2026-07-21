package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type apiLogRepoStub struct {
	systemItems     []governanceapp.AdminSystemLogView
	operationItems  []governanceapp.AdminOperationLogView
	systemLists     int
	operationLists  int
	cleanupCategory string
	cleanupCalls    int
	cleanupAudit    *governancedomain.OperationLog
}

func (s *apiLogRepoStub) ListSystemLogs(context.Context, governanceapp.AdminLogListFilter) ([]governanceapp.AdminSystemLogView, int64, error) {
	s.systemLists++
	return s.systemItems, int64(len(s.systemItems)), nil
}

func (s *apiLogRepoStub) ListOperationLogs(context.Context, governanceapp.AdminLogListFilter) ([]governanceapp.AdminOperationLogView, int64, error) {
	s.operationLists++
	return s.operationItems, int64(len(s.operationItems)), nil
}

func (s *apiLogRepoStub) CountSystemLogs(context.Context) (int64, error) {
	return int64(len(s.systemItems)), nil
}

func (s *apiLogRepoStub) CountOperationLogs(context.Context) (int64, error) {
	return int64(len(s.operationItems)), nil
}

func (s *apiLogRepoStub) CleanupLogs(_ context.Context, category string, _ time.Time, audit *governancedomain.OperationLog) (int64, error) {
	s.cleanupCalls++
	s.cleanupCategory = category
	if audit != nil {
		copied := *audit
		s.cleanupAudit = &copied
	}
	if category == governanceapp.AdminLogCategorySystem {
		return 2, nil
	}
	return 3, nil
}

type logPermissionChecker struct {
	allow    bool
	resource string
	action   string
	calls    int
}

func (c *logPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	c.calls++
	c.resource = resource
	c.action = action
	return c.allow, nil
}

func TestAdminLogRoutesRequireSessionAndPermission(t *testing.T) {
	for _, path := range []string{"/v1/admin/logs/system", "/v1/admin/logs/operations"} {
		t.Run(path+"/session", func(t *testing.T) {
			repo := &apiLogRepoStub{}
			checker := &logPermissionChecker{allow: true}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			adminLogTestRouter(repo, checker, iamdomain.RoleAdmin).ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.Zero(t, checker.calls)
			require.Zero(t, repo.systemLists+repo.operationLists)
		})

		t.Run(path+"/permission", func(t *testing.T) {
			repo := &apiLogRepoStub{}
			checker := &logPermissionChecker{allow: false}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
			response := httptest.NewRecorder()
			adminLogTestRouter(repo, checker, iamdomain.RoleAdmin).ServeHTTP(response, request)
			require.Equal(t, http.StatusForbidden, response.Code)
			require.Equal(t, "governance:log", checker.resource)
			require.Equal(t, "read", checker.action)
			require.Zero(t, repo.systemLists+repo.operationLists)
		})
	}
}

func TestAdminLogRoutesReturnOperatorAndSafeDiagnosticFields(t *testing.T) {
	now := time.Date(2026, time.July, 20, 8, 0, 0, 0, time.UTC)
	repo := &apiLogRepoStub{
		systemItems: []governanceapp.AdminSystemLogView{{
			ID: 11, CreatedAt: now, Level: "warning", Module: "mailtransport",
			EventType: "resource.validation_failed", BizType: "resource", BizID: "61",
			Message: "Resource validation failed.", Detail: "category=authorization", RequestID: "req-system",
		}},
		operationItems: []governanceapp.AdminOperationLogView{{
			ID: 12, CreatedAt: now, OperatorUserID: 7, Operator: "ops@test.local",
			OperationType: "core.resource.validate", ResourceType: "resource", ResourceID: "61",
			Path: "/v1/admin/resources/:resourceId/validate", Result: "success",
			SafeSummary: "Resource validation accepted.", RequestID: "req-operation",
		}},
	}
	checker := &logPermissionChecker{allow: true}
	router := adminLogTestRouter(repo, checker, iamdomain.RoleAdmin)

	for _, testCase := range []struct {
		path string
		want string
	}{
		{path: "/v1/admin/logs/system?level=warning&limit=20", want: `"category":"system"`},
		{path: "/v1/admin/logs/operations?result=success&limit=20", want: `"operator":"ops@test.local"`},
	} {
		request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), testCase.want)
		require.Contains(t, response.Body.String(), `"facets":{"system":1,"operation":1}`)
		for _, forbidden := range []string{"password", "refreshToken", "objectKey", "claimToken"} {
			require.NotContains(t, response.Body.String(), forbidden)
		}
	}
}

func TestAdminLogRoutesRejectInvalidFilters(t *testing.T) {
	router := adminLogTestRouter(&apiLogRepoStub{}, &logPermissionChecker{allow: true}, iamdomain.RoleAdmin)
	for _, path := range []string{
		"/v1/admin/logs/system?level=fatal",
		"/v1/admin/logs/system?from=not-a-date",
		"/v1/admin/logs/operations?result=pending",
		"/v1/admin/logs/operations?limit=101",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code, path)
		require.Contains(t, response.Body.String(), "Invalid request parameters.")
	}
}

func TestAdminLogCleanupRequiresCSRFAndSuperAdmin(t *testing.T) {
	path := "/v1/admin/logs/system?before=2030-01-01T00:00:00Z"

	t.Run("operate permission required", func(t *testing.T) {
		repo := &apiLogRepoStub{}
		checker := &logPermissionChecker{allow: false}
		request := adminLogDeleteRequest(path, true)
		response := httptest.NewRecorder()
		adminLogTestRouter(repo, checker, iamdomain.RoleSuperAdmin).ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Equal(t, "governance:log", checker.resource)
		require.Equal(t, "operate", checker.action)
		require.Zero(t, repo.cleanupCalls)
	})

	t.Run("admin forbidden", func(t *testing.T) {
		repo := &apiLogRepoStub{}
		request := adminLogDeleteRequest(path, true)
		response := httptest.NewRecorder()
		adminLogTestRouter(repo, &logPermissionChecker{allow: true}, iamdomain.RoleAdmin).ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Zero(t, repo.cleanupCalls)
	})

	t.Run("cutoff required", func(t *testing.T) {
		repo := &apiLogRepoStub{}
		request := adminLogDeleteRequest("/v1/admin/logs/system", true)
		response := httptest.NewRecorder()
		adminLogTestRouter(repo, &logPermissionChecker{allow: true}, iamdomain.RoleSuperAdmin).ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Zero(t, repo.cleanupCalls)
	})

	t.Run("csrf required", func(t *testing.T) {
		repo := &apiLogRepoStub{}
		request := adminLogDeleteRequest(path, false)
		response := httptest.NewRecorder()
		adminLogTestRouter(repo, &logPermissionChecker{allow: true}, iamdomain.RoleSuperAdmin).ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Zero(t, repo.cleanupCalls)
	})

	t.Run("future cutoff allowed and scoped", func(t *testing.T) {
		repo := &apiLogRepoStub{}
		checker := &logPermissionChecker{allow: true}
		request := adminLogDeleteRequest(path, true)
		request.Header.Set("X-Request-ID", "req-cleanup")
		response := httptest.NewRecorder()
		adminLogTestRouter(repo, checker, iamdomain.RoleSuperAdmin).ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		require.JSONEq(t, `{"removed":2}`, response.Body.String())
		require.Equal(t, "operate", checker.action)
		require.Equal(t, 1, repo.cleanupCalls)
		require.Equal(t, governanceapp.AdminLogCategorySystem, repo.cleanupCategory)
		require.NotNil(t, repo.cleanupAudit)
		require.Equal(t, "req-cleanup", repo.cleanupAudit.RequestID)
	})
}

func adminLogDeleteRequest(path string, csrf bool) *http.Request {
	request := httptest.NewRequest(http.MethodDelete, path, nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
	if csrf {
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf-token"})
		request.Header.Set(middleware.CSRFHeaderName, "csrf-token")
	}
	return request
}

func adminLogTestRouter(repo governanceapp.AdminLogRepository, checker middleware.PermissionChecker, role iamdomain.Role) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	module := &Module{Logs: governanceapp.NewAdminLogService(repo)}
	RegisterRoutes(router.Group("/v1"), module, middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 7, role, "admin@test.local", true
	}), checker)
	return router
}
