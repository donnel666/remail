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

func TestAdminAliasExpediteRouteRequiresSessionPermissionCSRFAndIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("session", func(t *testing.T) {
		router, _, _ := newAdminAliasExpediteTestRouter(false, nil)
		response := performAdminAliasExpediteRequest(router, false, true, true)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	})
	t.Run("permission", func(t *testing.T) {
		router, _, checker := newAdminAliasExpediteTestRouter(false, nil)
		response := performAdminAliasExpediteRequest(router, true, true, true)
		require.Equal(t, http.StatusForbidden, response.Code)
		assert.Equal(t, "core:resource", checker.resource)
		assert.Equal(t, "operate", checker.action)
	})
	t.Run("csrf", func(t *testing.T) {
		router, _, _ := newAdminAliasExpediteTestRouter(true, nil)
		response := performAdminAliasExpediteRequest(router, true, false, true)
		require.Equal(t, http.StatusForbidden, response.Code)
	})
	t.Run("idempotency key", func(t *testing.T) {
		router, _, _ := newAdminAliasExpediteTestRouter(true, nil)
		response := performAdminAliasExpediteRequest(router, true, true, false)
		require.Equal(t, http.StatusBadRequest, response.Code)
		assert.Contains(t, response.Body.String(), "Idempotency-Key is required.")
	})
	t.Run("idempotency key length", func(t *testing.T) {
		router, _, _ := newAdminAliasExpediteTestRouter(true, nil)
		response := performAdminAliasExpediteRequestWithKey(router, true, true, strings.Repeat("x", 129))
		require.Equal(t, http.StatusBadRequest, response.Code)
		assert.Contains(t, response.Body.String(), "Invalid Idempotency-Key.")
	})
}

func TestAdminAliasExpediteRouteReturnsOpenAPITaskShapeWithoutInternalFacts(t *testing.T) {
	now := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
	router, store, checker := newAdminAliasExpediteTestRouter(true, &mailapp.MicrosoftAliasExpediteResult{
		ResourceID: 100,
		Status:     "queued",
		QueuedAt:   now,
		UpdatedAt:  now,
	})
	response := performAdminAliasExpediteRequest(router, true, true, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	assert.Equal(t, "core:resource", checker.resource)
	assert.Equal(t, "operate", checker.action)
	assert.Equal(t, uint(100), store.command.ResourceID)
	assert.Equal(t, uint(7), store.command.OperatorUserID)
	assert.Equal(t, "alias-expedite-idempotency", store.command.IdempotencyKey)
	assert.Equal(t, "test-request", store.command.RequestID)
	assert.Equal(t, "/v1/admin/resources/:resourceId/aliases", store.command.Path)
	require.NotNil(t, store.operationLog)
	assert.Equal(t, "mailtransport.microsoft_alias.expedite", store.operationLog.OperationType)
	assert.Equal(t, "100", store.operationLog.ResourceID)

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "alias_schedule:100", body["taskId"])
	assert.Equal(t, "test-request", body["requestId"])
	assert.Equal(t, "queued", body["status"])
	assert.Equal(t, float64(1), body["accepted"])
	assert.Equal(t, false, body["reused"])
	task, ok := body["task"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alias_schedule:100", task["taskId"])
	assert.Equal(t, "microsoft_resource", task["bizType"])
	assert.Equal(t, "alias", task["kind"])
	assert.Equal(t, "queued", task["status"])
	assert.Equal(t, float64(0), task["attempts"])
	assert.Equal(t, float64(1), task["maxAttempts"])
	assert.Equal(t, float64(1), task["remainingAttempts"])
	assert.Nil(t, task["credentialRevision"])
	assert.Nil(t, task["progress"])

	serialized := strings.ToLower(response.Body.String())
	for _, forbidden := range []string{
		"password",
		"clientid",
		"refreshtoken",
		"accesstoken",
		"candidate",
		"claimtoken",
		"dispatchtoken",
		"lastsafeerror",
		"path",
	} {
		assert.NotContains(t, serialized, forbidden)
	}
}

func TestAdminAliasExpediteRouteUsesSafeErrorContract(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{name: "not found", err: mailapp.ErrMicrosoftAliasResourceNotFound, status: http.StatusNotFound, body: "Resource not found."},
		{name: "idempotency", err: mailapp.ErrMicrosoftAliasIdempotencyConflict, status: http.StatusConflict, body: "Idempotency-Key conflicts"},
		{name: "missing canonical schedule", err: mailapp.ErrMicrosoftAliasScheduleNotFound, status: http.StatusConflict, body: "Resource state does not allow alias creation."},
		{name: "unavailable", err: mailapp.ErrMicrosoftAliasAdminUnavailable, status: http.StatusServiceUnavailable, body: "Mail service is temporarily unavailable."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, store, _ := newAdminAliasExpediteTestRouter(true, nil)
			store.err = test.err
			response := performAdminAliasExpediteRequest(router, true, true, true)
			assert.Equal(t, test.status, response.Code)
			assert.Contains(t, response.Body.String(), test.body)
			assert.NotContains(t, response.Body.String(), "database")
		})
	}
}

type adminAliasExpediteStoreStub struct {
	mailapp.MicrosoftAliasScheduleStore
	result       *mailapp.MicrosoftAliasExpediteResult
	err          error
	receiptReuse bool
	command      mailapp.MicrosoftAliasExpediteCommand
	operationLog *governancedomain.OperationLog
}

func (s *adminAliasExpediteStoreStub) AcceptAdminAliasExpedite(
	_ context.Context,
	command mailapp.MicrosoftAliasExpediteCommand,
	_ time.Time,
	operationLog *governancedomain.OperationLog,
) (*mailapp.MicrosoftAliasExpediteResult, bool, error) {
	s.command = command
	if operationLog != nil {
		clone := *operationLog
		s.operationLog = &clone
	}
	if s.err != nil {
		return nil, false, s.err
	}
	if s.result == nil {
		now := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
		return &mailapp.MicrosoftAliasExpediteResult{ResourceID: command.ResourceID, Status: "queued", QueuedAt: now, UpdatedAt: now}, s.receiptReuse, nil
	}
	clone := *s.result
	return &clone, s.receiptReuse, nil
}

type adminAliasExpeditePermissionChecker struct {
	allowed  bool
	resource string
	action   string
}

func (c *adminAliasExpeditePermissionChecker) Check(
	_ context.Context,
	_ uint,
	_ iamdomain.Role,
	resource string,
	action string,
) (bool, error) {
	c.resource = resource
	c.action = action
	return c.allowed, nil
}

func newAdminAliasExpediteTestRouter(
	allowed bool,
	result *mailapp.MicrosoftAliasExpediteResult,
) (*gin.Engine, *adminAliasExpediteStoreStub, *adminAliasExpeditePermissionChecker) {
	store := &adminAliasExpediteStoreStub{result: result}
	service := mailapp.NewMicrosoftAliasService(store, nil, nil)
	checker := &adminAliasExpeditePermissionChecker{allowed: allowed}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterMailTransportRoutes(
		router.Group("/v1"),
		&MailTransportModule{MicrosoftAliases: service},
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 7, iamdomain.RoleAdmin, "admin@test.local", true
		}),
		checker,
	)
	return router, store, checker
}

func performAdminAliasExpediteRequest(
	router *gin.Engine,
	authenticated bool,
	csrf bool,
	idempotency bool,
) *httptest.ResponseRecorder {
	key := ""
	if idempotency {
		key = "alias-expedite-idempotency"
	}
	return performAdminAliasExpediteRequestWithKey(router, authenticated, csrf, key)
}

func performAdminAliasExpediteRequestWithKey(
	router *gin.Engine,
	authenticated bool,
	csrf bool,
	idempotencyKey string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/resources/100/aliases", nil)
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
