package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminResourceFetchRouteRequiresAuthPermissionCSRFAndIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("session", func(t *testing.T) {
		router, _, _ := newAdminResourceFetchTestRouter(false)
		response := performAdminResourceFetchRequest(router, false, true, true)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	})

	t.Run("permission", func(t *testing.T) {
		router, _, checker := newAdminResourceFetchTestRouter(false)
		response := performAdminResourceFetchRequest(router, true, true, true)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Equal(t, "mailmatch:message", checker.resource)
		require.Equal(t, "operate", checker.action)
	})

	t.Run("csrf", func(t *testing.T) {
		router, _, _ := newAdminResourceFetchTestRouter(true)
		response := performAdminResourceFetchRequest(router, true, false, true)
		require.Equal(t, http.StatusForbidden, response.Code)
	})

	t.Run("idempotency key", func(t *testing.T) {
		router, _, _ := newAdminResourceFetchTestRouter(true)
		response := performAdminResourceFetchRequest(router, true, true, false)
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Contains(t, response.Body.String(), "Idempotency-Key is required.")
	})

	t.Run("idempotency key length", func(t *testing.T) {
		router, _, _ := newAdminResourceFetchTestRouter(true)
		response := performAdminResourceFetchRequestWithKey(router, true, true, strings.Repeat("x", 129))
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Contains(t, response.Body.String(), "Invalid request parameters.")
	})
}

func TestAdminResourceFetchRouteReturnsOpenAPITaskShapeWithoutSecrets(t *testing.T) {
	router, repo, checker := newAdminResourceFetchTestRouter(true)
	response := performAdminResourceFetchRequest(router, true, true, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Equal(t, "mailmatch:message", checker.resource)
	require.Equal(t, "operate", checker.action)
	require.NotNil(t, repo.operationLog)
	require.Equal(t, "mailmatch.admin_resource.fetch", repo.operationLog.OperationType)
	require.Equal(t, "100", repo.operationLog.ResourceID)
	require.Equal(t, "success", repo.operationLog.Result)

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "fetch:42", body["taskId"])
	require.Equal(t, "test-request", body["requestId"])
	require.Equal(t, "queued", body["status"])
	require.Equal(t, float64(1), body["accepted"])
	require.Equal(t, false, body["reused"])
	task, ok := body["task"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fetch:42", task["taskId"])
	require.Equal(t, "microsoft_resource", task["bizType"])
	require.Equal(t, "fetch", task["kind"])
	require.Equal(t, "queued", task["status"])
	require.Equal(t, float64(7), task["credentialRevision"])
	require.Contains(t, task, "progress")
	require.Nil(t, task["progress"])

	serialized := strings.ToLower(response.Body.String())
	for _, forbidden := range []string{
		"password-canary",
		"client-canary",
		"refresh-token-canary",
		"main@example.com",
		"claimtoken",
		"dispatchtoken",
		"lastsafeerror",
		"path",
	} {
		require.NotContains(t, serialized, forbidden)
	}
}

func TestAdminResourceProjectScanReusesDurableFetchTaskState(t *testing.T) {
	router, repo, checker := newAdminResourceFetchTestRouter(true)
	response := performAdminResourceHistoryRequest(router)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Equal(t, "core:resource", checker.resource)
	require.Equal(t, "operate", checker.action)
	require.NotNil(t, repo.operationLog)
	require.Equal(t, "mailmatch.admin_resource.history_scan", repo.operationLog.OperationType)

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	task := body["task"].(map[string]any)
	require.Equal(t, "history", task["kind"])
	require.Equal(t, "fetch:42", task["taskId"])
}

func newAdminResourceFetchTestRouter(allowed bool) (*gin.Engine, *adminResourceFetchRepoStub, *adminResourceFetchPermissionChecker) {
	repo := &adminResourceFetchRepoStub{}
	queue := &adminResourceFetchQueueStub{}
	useCase := mailmatchapp.NewResourceFetchUseCase(repo, queue, nil, nil, adminResourceFetchSystemLogsStub{})
	module := &Module{ResourceFetch: useCase}
	checker := &adminResourceFetchPermissionChecker{allowed: allowed}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterAdminRoutes(
		router.Group("/v1"),
		module,
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, domain.Role, string, bool) {
			return 1, domain.RoleAdmin, "admin@test.local", true
		}),
		checker,
	)
	return router, repo, checker
}

func performAdminResourceFetchRequest(router *gin.Engine, authenticated bool, csrf bool, idempotency bool) *httptest.ResponseRecorder {
	key := ""
	if idempotency {
		key = "resource-fetch-idempotency"
	}
	return performAdminResourceFetchRequestWithKey(router, authenticated, csrf, key)
}

func performAdminResourceFetchRequestWithKey(router *gin.Engine, authenticated bool, csrf bool, idempotencyKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/resources/100/messages/fetch", nil)
	req.Header.Set("X-Request-ID", "test-request")
	if authenticated {
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	}
	if csrf {
		req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
		req.Header.Set(middleware.CSRFHeaderName, "csrf")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func performAdminResourceHistoryRequest(router *gin.Engine) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/resources/100/projects/scan", nil)
	req.Header.Set("X-Request-ID", "test-history-request")
	req.Header.Set("Idempotency-Key", "resource-history-idempotency")
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	req.Header.Set(middleware.CSRFHeaderName, "csrf")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

type adminResourceFetchPermissionChecker struct {
	allowed  bool
	resource string
	action   string
}

func (c *adminResourceFetchPermissionChecker) Check(_ context.Context, _ uint, _ domain.Role, resource string, action string) (bool, error) {
	c.resource = resource
	c.action = action
	return c.allowed, nil
}

type adminResourceFetchRepoStub struct {
	operationLog *governancedomain.OperationLog
}

func (r *adminResourceFetchRepoStub) CreateOrReuseResourceFetch(_ context.Context, job *mailmatchdomain.ResourceFetchJob, log *governancedomain.OperationLog) (bool, error) {
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	job.ID = 42
	job.ExpectedCredentialRevision = 7
	job.Recipient = "main@example.com"
	job.CreatedAt = now
	job.UpdatedAt = now
	copyLog := *log
	r.operationLog = &copyLog
	return false, nil
}

func (*adminResourceFetchRepoStub) FindResourceFetchJob(context.Context, uint) (*mailmatchdomain.ResourceFetchJob, error) {
	return nil, nil
}

func (*adminResourceFetchRepoStub) ClaimDispatchableResourceFetches(context.Context, int, time.Time, time.Time) ([]mailmatchdomain.ResourceFetchJob, error) {
	return nil, nil
}

func (*adminResourceFetchRepoStub) MarkResourceFetchRunning(context.Context, uint, string) (string, bool, error) {
	return "", false, nil
}

func (*adminResourceFetchRepoStub) ReleaseResourceFetchDispatch(context.Context, uint, string) error {
	return nil
}

func (*adminResourceFetchRepoStub) MarkResourceFetchDispatchFailed(context.Context, uint, string, string, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) LoadResourceFetchScope(context.Context, uint, uint64) (*mailmatchdomain.ResourceFetchScope, error) {
	return nil, nil
}

func (*adminResourceFetchRepoStub) AssertResourceFetchFence(context.Context, uint, string, uint, uint64) error {
	return nil
}

func (*adminResourceFetchRepoStub) CompleteResourceFetch(context.Context, uint, string, uint, uint64, string, int, int, int, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) CompleteResourceFetchTask(context.Context, uint, string, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) MarkResourceFetchCanceled(context.Context, uint, string, string, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) MarkResourceFetchFailure(context.Context, uint, string, string, bool, time.Time, *governancedomain.SystemLog) (bool, error) {
	return false, nil
}

type adminResourceFetchQueueStub struct{}

func (adminResourceFetchQueueStub) EnqueueResourceFetch(context.Context, mailmatchapp.ResourceFetchTask) error {
	return nil
}

func (adminResourceFetchQueueStub) EnqueueFetchDispatcher(context.Context, time.Duration) error {
	return nil
}

type adminResourceFetchSystemLogsStub struct{}

func (adminResourceFetchSystemLogsStub) Create(context.Context, *governancedomain.SystemLog) error {
	return nil
}
