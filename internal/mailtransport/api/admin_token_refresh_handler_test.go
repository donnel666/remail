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
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminTokenRefreshRouteRequiresSessionPermissionCSRFAndIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("session", func(t *testing.T) {
		router, _, _ := newAdminTokenRefreshTestRouter(false, nil)
		response := performAdminTokenRefreshRequest(router, false, true, true)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	})
	t.Run("permission", func(t *testing.T) {
		router, _, checker := newAdminTokenRefreshTestRouter(false, nil)
		response := performAdminTokenRefreshRequest(router, true, true, true)
		assert.Equal(t, http.StatusForbidden, response.Code)
		assert.Equal(t, "core:resource", checker.resource)
		assert.Equal(t, "operate", checker.action)
	})
	t.Run("csrf", func(t *testing.T) {
		router, _, _ := newAdminTokenRefreshTestRouter(true, nil)
		response := performAdminTokenRefreshRequest(router, true, false, true)
		assert.Equal(t, http.StatusForbidden, response.Code)
	})
	t.Run("idempotency key", func(t *testing.T) {
		router, _, _ := newAdminTokenRefreshTestRouter(true, nil)
		response := performAdminTokenRefreshRequest(router, true, true, false)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Contains(t, response.Body.String(), "Idempotency-Key is required.")
	})
	t.Run("idempotency key length", func(t *testing.T) {
		router, _, _ := newAdminTokenRefreshTestRouter(true, nil)
		response := performAdminTokenRefreshRequestWithKey(router, true, true, strings.Repeat("x", 129))
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Contains(t, response.Body.String(), "Invalid Idempotency-Key.")
	})
}

func TestAdminTokenRefreshRouteReturnsOpenAPITaskShapeWithoutSecrets(t *testing.T) {
	now := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
	state := &mailapp.MicrosoftTokenRefreshState{
		ResourceID:                 100,
		Generation:                 3,
		ExpectedCredentialRevision: 7,
		Status:                     mailapp.MicrosoftTokenRefreshPending,
		Failures:                   0,
		LastSafeError:              "refresh-token-canary",
		RequestID:                  "internal-request-canary",
		Path:                       "internal-path-canary",
		RequestedAt:                &now,
		UpdatedAt:                  now,
	}
	router, repo, checker := newAdminTokenRefreshTestRouter(true, state)
	response := performAdminTokenRefreshRequest(router, true, true, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	assert.Equal(t, "core:resource", checker.resource)
	assert.Equal(t, "operate", checker.action)
	assert.Equal(t, uint(100), repo.command.ResourceID)
	assert.Equal(t, uint(7), repo.command.OperatorUserID)
	assert.Equal(t, "token-refresh-idempotency", repo.command.IdempotencyKey)
	assert.Equal(t, "test-request", repo.command.RequestID)
	assert.Equal(t, "/v1/admin/resources/:resourceId/token/refresh", repo.command.Path)
	require.NotNil(t, repo.operationLog)
	assert.Equal(t, "mailtransport.microsoft_token_refresh.accept", repo.operationLog.OperationType)
	assert.Equal(t, "100", repo.operationLog.ResourceID)

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "token:100", body["taskId"])
	assert.Equal(t, "internal-request-canary", body["requestId"])
	assert.Equal(t, "queued", body["status"])
	assert.Equal(t, float64(1), body["accepted"])
	assert.Equal(t, false, body["reused"])
	task, ok := body["task"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "token:100", task["taskId"])
	assert.Equal(t, "microsoft_resource", task["bizType"])
	assert.Equal(t, "token", task["kind"])
	assert.Equal(t, "queued", task["status"])
	assert.Equal(t, float64(7), task["credentialRevision"])
	assert.Equal(t, float64(3), task["remainingAttempts"])
	assert.Nil(t, task["progress"])

	serialized := strings.ToLower(response.Body.String())
	for _, forbidden := range []string{
		"password",
		"clientid",
		"refreshtoken",
		"accesstoken",
		"claimtoken",
		"dispatchtoken",
		"lastsafeerror",
		"internal-path-canary",
	} {
		assert.NotContains(t, serialized, forbidden)
	}
}

func TestAdminTokenRefreshRouteUsesSafeErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{name: "not found", err: mailapp.ErrMicrosoftTokenRefreshNotFound, status: http.StatusNotFound, body: "Resource not found."},
		{name: "deleted", err: mailapp.ErrMicrosoftTokenRefreshConflict, status: http.StatusConflict, body: "Resource state does not allow token refresh."},
		{name: "idempotency", err: mailapp.ErrMicrosoftAdminIdempotencyConflict, status: http.StatusConflict, body: "Idempotency-Key conflicts"},
		{name: "credentials", err: mailapp.ErrMicrosoftTokenCredentialsMissing, status: http.StatusUnprocessableEntity, body: "Microsoft token credentials are incomplete."},
		{name: "unavailable", err: mailapp.ErrMicrosoftTokenRefreshUnavailable, status: http.StatusServiceUnavailable, body: "Mail service is temporarily unavailable."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, repo, _ := newAdminTokenRefreshTestRouter(true, nil)
			repo.createErr = test.err
			response := performAdminTokenRefreshRequest(router, true, true, true)
			assert.Equal(t, test.status, response.Code)
			assert.Contains(t, response.Body.String(), test.body)
			assert.NotContains(t, response.Body.String(), "database")
		})
	}
}

type adminTokenRefreshRepoStub struct {
	state              *mailapp.MicrosoftTokenRefreshState
	createErr          error
	command            mailapp.MicrosoftTokenRefreshCommand
	operationLog       *governancedomain.OperationLog
	releaseCalls       int
	releasedID         uint
	releasedGeneration uint64
	execution          *mailapp.MicrosoftTokenRefreshExecution
	claimed            bool
}

func (r *adminTokenRefreshRepoStub) Request(
	_ context.Context,
	command mailapp.MicrosoftTokenRefreshCommand,
	operationLog *governancedomain.OperationLog,
) (*mailapp.MicrosoftTokenRefreshState, bool, error) {
	r.command = command
	if operationLog != nil {
		clone := *operationLog
		r.operationLog = &clone
	}
	if r.createErr != nil {
		return nil, false, r.createErr
	}
	if r.state == nil {
		now := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
		return &mailapp.MicrosoftTokenRefreshState{
			ResourceID:                 command.ResourceID,
			Generation:                 1,
			ExpectedCredentialRevision: 7,
			Status:                     mailapp.MicrosoftTokenRefreshPending,
			RequestedAt:                &now,
			UpdatedAt:                  now,
		}, false, nil
	}
	clone := *r.state
	return &clone, false, nil
}

func (*adminTokenRefreshRepoStub) ListPending(context.Context, int) ([]mailapp.MicrosoftTokenRefreshState, error) {
	return nil, nil
}

func (*adminTokenRefreshRepoStub) MarkProcessing(context.Context, uint, uint64) (bool, error) {
	return true, nil
}

func (r *adminTokenRefreshRepoStub) ReleaseInfrastructureFailure(_ context.Context, id uint, generation uint64, _ string) (bool, error) {
	r.releaseCalls++
	r.releasedID = id
	r.releasedGeneration = generation
	return true, nil
}

func (r *adminTokenRefreshRepoStub) LoadExecution(context.Context, mailapp.MicrosoftTokenRefreshTask) (*mailapp.MicrosoftTokenRefreshExecution, bool, error) {
	return r.execution, r.claimed, nil
}

func (*adminTokenRefreshRepoStub) RecordRetryableFailure(context.Context, mailapp.MicrosoftTokenRefreshTask, string) (bool, error) {
	return false, nil
}

func (*adminTokenRefreshRepoStub) ApplyResult(context.Context, mailapp.MicrosoftTokenRefreshTask, mailapp.MicrosoftTokenRefreshProtocolResult) error {
	return nil
}

type adminTokenRefreshQueueStub struct{}

func (adminTokenRefreshQueueStub) EnqueueMicrosoftTokenRefresh(context.Context, mailapp.MicrosoftTokenRefreshTask) (bool, error) {
	return true, nil
}

func (adminTokenRefreshQueueStub) EnqueueMicrosoftTokenRefreshDispatcher(context.Context, time.Duration) error {
	return nil
}

func newAdminTokenRefreshTestRouter(
	allowed bool,
	state *mailapp.MicrosoftTokenRefreshState,
) (*gin.Engine, *adminTokenRefreshRepoStub, *adminAliasExpeditePermissionChecker) {
	repo := &adminTokenRefreshRepoStub{state: state}
	service := mailapp.NewMicrosoftTokenRefreshService(repo, adminTokenRefreshQueueStub{}, nil)
	checker := &adminAliasExpeditePermissionChecker{allowed: allowed}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterMailTransportRoutes(
		router.Group("/v1"),
		&MailTransportModule{TokenRefresh: service},
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 7, iamdomain.RoleAdmin, "admin@test.local", true
		}),
		checker,
	)
	return router, repo, checker
}

func performAdminTokenRefreshRequest(
	router *gin.Engine,
	authenticated bool,
	csrf bool,
	idempotency bool,
) *httptest.ResponseRecorder {
	key := ""
	if idempotency {
		key = "token-refresh-idempotency"
	}
	return performAdminTokenRefreshRequestWithKey(router, authenticated, csrf, key)
}

func performAdminTokenRefreshRequestWithKey(
	router *gin.Engine,
	authenticated bool,
	csrf bool,
	idempotencyKey string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/resources/100/token/refresh", nil)
	request.Header.Set("X-Request-ID", "test-request")
	if authenticated {
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	}
	if csrf {
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "csrf"})
		request.Header.Set(middleware.CSRFHeaderName, "csrf")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
