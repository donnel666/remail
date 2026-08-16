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
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
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
	require.Equal(t, "fetch:100", body["taskId"])
	require.Equal(t, "test-request", body["requestId"])
	require.Equal(t, "queued", body["status"])
	require.Equal(t, float64(1), body["accepted"])
	require.Equal(t, false, body["reused"])
	task, ok := body["task"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fetch:100", task["taskId"])
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

func TestAdminResourceFetchRouteAcceptsICloudType(t *testing.T) {
	router, repo, _ := newAdminResourceFetchTestRouter(true)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/resources/100/messages/fetch?type=icloud", nil)
	req.Header.Set("X-Request-ID", "icloud-fetch-request")
	req.Header.Set("Idempotency-Key", "icloud-fetch-idempotency")
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	req.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
	req.Header.Set(middleware.CSRFHeaderName, "csrf")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Equal(t, "mailmatch.admin_icloud_resource.fetch", repo.operationLog.OperationType)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "icloud_resource", body["task"].(map[string]any)["bizType"])
}

func TestAdminResourceProjectScanUsesIndependentHistoryTaskIdentity(t *testing.T) {
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
	require.Equal(t, "resource_history:100", task["taskId"])
}

type resourceFetchBackgroundGateStub struct {
	admitted bool
	calls    int
}

func (g *resourceFetchBackgroundGateStub) TryAcquire() (func(), bool) {
	g.calls++
	return func() {}, g.admitted
}

func TestResourceFetchHandlersKeepForegroundAdminFetchOutOfBackgroundGate(t *testing.T) {
	repo := &adminResourceFetchRepoStub{}
	useCase := mailmatchapp.NewAdminResourceFetchUseCase(repo, adminResourceFetchQueueStub{}, nil, nil, nil)
	gate := &resourceFetchBackgroundGateStub{admitted: false}
	mux := asynq.NewServeMux()
	RegisterTaskHandlers(mux, &Module{AdminResourceFetch: useCase, BackgroundExecution: gate})

	payload, err := json.Marshal(mailmatchapp.AdminResourceFetchTask{ResourceID: 100, Generation: 1})
	require.NoError(t, err)
	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailmatchinfra.TypeMailmatchAdminResourceFetch, payload))

	require.NoError(t, err)
	require.Zero(t, gate.calls)
	require.Equal(t, 1, repo.markCalls)
}

func TestResourceHistoryHandlerUsesBackgroundGate(t *testing.T) {
	repo := &adminResourceFetchRepoStub{}
	useCase := mailmatchapp.NewResourceHistoryUseCase(repo, adminResourceFetchQueueStub{}, nil, nil)
	gate := &resourceFetchBackgroundGateStub{admitted: false}
	mux := asynq.NewServeMux()
	RegisterTaskHandlers(mux, &Module{ResourceHistory: useCase, BackgroundExecution: gate})

	payload, err := json.Marshal(mailmatchapp.ResourceHistoryTask{ResourceID: 100, Generation: 1})
	require.NoError(t, err)
	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailmatchinfra.TypeMailmatchResourceHistory, payload))

	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.Equal(t, 1, gate.calls)
	require.Zero(t, repo.markCalls)
}

func TestAdminResourceFetchHandlerKeepsJSONDecodeError(t *testing.T) {
	repo := &adminResourceFetchRepoStub{}
	useCase := mailmatchapp.NewAdminResourceFetchUseCase(repo, adminResourceFetchQueueStub{}, nil, nil, nil)
	mux := asynq.NewServeMux()
	RegisterTaskHandlers(mux, &Module{AdminResourceFetch: useCase})

	err := mux.ProcessTask(context.Background(), asynq.NewTask(mailmatchinfra.TypeMailmatchAdminResourceFetch, []byte("{")))

	require.ErrorIs(t, err, asynq.SkipRetry)
	require.Contains(t, err.Error(), "unexpected end of JSON input")
}

func newAdminResourceFetchTestRouter(allowed bool) (*gin.Engine, *adminResourceFetchRepoStub, *adminResourceFetchPermissionChecker) {
	repo := &adminResourceFetchRepoStub{}
	queue := &adminResourceFetchQueueStub{}
	adminFetch := mailmatchapp.NewAdminResourceFetchUseCase(repo, queue, nil, nil, adminResourceFetchSystemLogsStub{})
	history := mailmatchapp.NewResourceHistoryUseCase(repo, queue, nil, adminResourceFetchSystemLogsStub{})
	module := &Module{AdminResourceFetch: adminFetch, ResourceHistory: history}
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
	markCalls    int
}

func (r *adminResourceFetchRepoStub) CreateOrReuseResourceFetch(_ context.Context, job *mailmatchdomain.ResourceFetchJob, log *governancedomain.OperationLog) (bool, error) {
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	job.ID = 42
	job.Generation = 3
	job.ExpectedCredentialRevision = 7
	job.Recipient = "main@example.com"
	job.CreatedAt = now
	job.UpdatedAt = now
	copyLog := *log
	r.operationLog = &copyLog
	return false, nil
}

func (*adminResourceFetchRepoStub) FindResourceFetch(context.Context, uint, uint64) (*mailmatchdomain.ResourceFetchJob, error) {
	return nil, nil
}

func (*adminResourceFetchRepoStub) ListPendingResourceFetches(context.Context, int) ([]mailmatchdomain.ResourceFetchJob, error) {
	return nil, nil
}

func (r *adminResourceFetchRepoStub) MarkResourceFetchProcessing(context.Context, uint, uint64) (bool, error) {
	r.markCalls++
	return false, nil
}

func (*adminResourceFetchRepoStub) ReleaseResourceFetchInfrastructureFailure(context.Context, uint, uint64, string, *governancedomain.SystemLog) (bool, error) {
	return false, nil
}

func (*adminResourceFetchRepoStub) LoadResourceFetchScope(context.Context, uint, uint64, mailmatchdomain.ResourceType) (*mailmatchdomain.ResourceFetchScope, error) {
	return nil, nil
}

func (*adminResourceFetchRepoStub) AssertResourceFetchFence(context.Context, uint, uint64, uint64) error {
	return nil
}

func (*adminResourceFetchRepoStub) CompleteResourceFetch(context.Context, uint, uint64, uint64, string, *bool, int, int, int, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) AssertICloudResourceFetchFence(context.Context, uint, uint64, uint64) error {
	return nil
}

func (*adminResourceFetchRepoStub) CompleteICloudResourceFetch(context.Context, uint, uint64, uint64, int, int, int, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) CompleteResourceFetchTask(context.Context, uint, uint64, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) MarkResourceFetchCanceled(context.Context, uint, uint64, string, time.Time, *governancedomain.SystemLog) error {
	return nil
}

func (*adminResourceFetchRepoStub) MarkResourceFetchFailure(context.Context, uint, uint64, string, bool, time.Time, *governancedomain.SystemLog) (bool, error) {
	return false, nil
}

type adminResourceFetchQueueStub struct{}

func (adminResourceFetchQueueStub) EnqueueAdminResourceFetch(context.Context, mailmatchapp.AdminResourceFetchTask) (bool, error) {
	return true, nil
}

func (adminResourceFetchQueueStub) EnqueueResourceHistory(context.Context, mailmatchapp.ResourceHistoryTask) (bool, error) {
	return true, nil
}

func (adminResourceFetchQueueStub) EnqueueAdminResourceFetchDispatcher(context.Context, time.Duration) error {
	return nil
}

func (adminResourceFetchQueueStub) EnqueueResourceHistoryDispatcher(context.Context, time.Duration) error {
	return nil
}

type adminResourceFetchSystemLogsStub struct{}

func (adminResourceFetchSystemLogsStub) Create(context.Context, *governancedomain.SystemLog) error {
	return nil
}
