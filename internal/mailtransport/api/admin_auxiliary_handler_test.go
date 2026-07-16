package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type auxiliaryQueryHandlerStub struct {
	page       *mailapp.AuxiliaryMailPage
	detail     *mailapp.AuxiliaryMessageDetail
	err        error
	listFilter mailapp.AuxiliaryMailFilter
	getRequest mailapp.AuxiliaryMailDetailRequest
}

func (s *auxiliaryQueryHandlerStub) List(_ context.Context, filter mailapp.AuxiliaryMailFilter) (*mailapp.AuxiliaryMailPage, error) {
	s.listFilter = filter
	return s.page, s.err
}

func (s *auxiliaryQueryHandlerStub) Get(_ context.Context, request mailapp.AuxiliaryMailDetailRequest) (*mailapp.AuxiliaryMessageDetail, error) {
	s.getRequest = request
	return s.detail, s.err
}

type auxiliarySessionFetcher struct{}

func (auxiliarySessionFetcher) FetchSession(context.Context, string) (uint, iamdomain.Role, string, bool) {
	return 7, iamdomain.RoleAdmin, "admin@example.com", true
}

type auxiliaryPermissionChecker struct {
	allowed bool
}

func (c auxiliaryPermissionChecker) Check(context.Context, uint, iamdomain.Role, string, string) (bool, error) {
	return c.allowed, nil
}

func TestAdminBindingsHandlerReturnsOnlySummaryContract(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	code := "123456"
	query := &auxiliaryQueryHandlerStub{page: &mailapp.AuxiliaryMailPage{
		Binding: &mailapp.AuxiliaryBindingSummary{
			ID:           5,
			EmailAddress: "proof@example.com",
			Status:       domain.MicrosoftBindingVerified,
			UpdatedAt:    receivedAt,
		},
		Items: []mailapp.AuxiliaryMessageSummary{{
			ID:               8,
			Recipient:        "proof@example.com",
			Sender:           "no-reply@microsoft.com",
			Subject:          "Security code",
			Preview:          "Security code 123456",
			Status:           mailapp.AuxiliaryMessageReceived,
			VerificationCode: &code,
			ReceivedAt:       receivedAt,
		}},
		Total:         1,
		TotalIncluded: true,
		Offset:        0,
		Limit:         20,
	}}
	recorder := serveAuxiliaryAdminRoute(t, query, true, "/v1/admin/bindings?resourceId=9")
	require.Equal(t, http.StatusOK, recorder.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	items, ok := body["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "123456", item["verificationCode"])
	assert.NotContains(t, item, "body")
	assert.NotContains(t, item, "matchDiagnostic")
	assert.NotContains(t, item, "objectKey")
	assert.NotContains(t, item, "raw")
	assert.Equal(t, float64(1), body["total"])
	assert.Equal(t, false, body["hasMore"])
}

func TestAdminBindingsHandlerAcceptsStableCursorAndSkipsRepeatedTotal(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	query := &auxiliaryQueryHandlerStub{page: &mailapp.AuxiliaryMailPage{
		Items:                []mailapp.AuxiliaryMessageSummary{{ID: 8, ReceivedAt: receivedAt}},
		Limit:                10,
		HasMore:              true,
		NextBeforeReceivedAt: &receivedAt,
		NextBeforeID:         8,
	}}
	recorder := serveAuxiliaryAdminRoute(t, query, true, "/v1/admin/bindings?resourceId=9&beforeReceivedAt=2026-07-12T12%3A00%3A00Z&beforeId=9&includeTotal=false&limit=10")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotNil(t, query.listFilter.BeforeReceivedAt)
	assert.Equal(t, receivedAt, *query.listFilter.BeforeReceivedAt)
	assert.Equal(t, uint(9), query.listFilter.BeforeID)
	assert.True(t, query.listFilter.SkipTotal)

	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.NotContains(t, body, "total")
	assert.Equal(t, true, body["hasMore"])
	assert.Equal(t, "2026-07-12T12:00:00Z", body["nextBeforeReceivedAt"])
	assert.Equal(t, float64(8), body["nextBeforeId"])
}

func TestAdminBindingMessageHandlerPassesAuditContextAndSafeDetail(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	query := &auxiliaryQueryHandlerStub{detail: &mailapp.AuxiliaryMessageDetail{
		AuxiliaryMessageSummary: mailapp.AuxiliaryMessageSummary{
			ID:         8,
			Recipient:  "proof@example.com",
			Sender:     "no-reply@microsoft.com",
			Subject:    "Security code",
			Preview:    "Security code 123456",
			Status:     mailapp.AuxiliaryMessageReceived,
			ReceivedAt: receivedAt,
		},
		Body: "Security code 123456",
	}}
	recorder := serveAuxiliaryAdminRoute(t, query, true, "/v1/admin/bindings/messages/8?resourceId=9")
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, uint(9), query.getRequest.ResourceID)
	assert.Equal(t, uint(8), query.getRequest.MessageID)
	assert.Equal(t, uint(7), query.getRequest.OperatorUserID)
	assert.Equal(t, "test-request", query.getRequest.RequestID)
	assert.Equal(t, "/v1/admin/bindings/messages/:messageId", query.getRequest.Path)
	assert.NotContains(t, recorder.Body.String(), "objectKey")
	assert.NotContains(t, recorder.Body.String(), "raw")
}

func TestAdminBindingRoutesRequirePermission(t *testing.T) {
	for _, target := range []string{
		"/v1/admin/bindings?resourceId=9",
		"/v1/admin/bindings/messages/8?resourceId=9",
	} {
		query := &auxiliaryQueryHandlerStub{page: &mailapp.AuxiliaryMailPage{Items: []mailapp.AuxiliaryMessageSummary{}, Limit: 20}}
		recorder := serveAuxiliaryAdminRoute(t, query, false, target)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "Permission denied.")
	}
}

func TestAdminBindingRoutesRequireSession(t *testing.T) {
	for _, target := range []string{
		"/v1/admin/bindings?resourceId=9",
		"/v1/admin/bindings/messages/8?resourceId=9",
	} {
		query := &auxiliaryQueryHandlerStub{page: &mailapp.AuxiliaryMailPage{Items: []mailapp.AuxiliaryMessageSummary{}, Limit: 20}}
		recorder := serveAuxiliaryAdminRouteWithAuth(t, query, true, false, target)
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
	}
}

func TestAdminBindingsRejectsLimitAboveContract(t *testing.T) {
	query := &auxiliaryQueryHandlerStub{page: &mailapp.AuxiliaryMailPage{Items: []mailapp.AuxiliaryMessageSummary{}, Limit: 20}}
	recorder := serveAuxiliaryAdminRoute(t, query, true, "/v1/admin/bindings?resourceId=9&limit=101")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Invalid request parameters.")
}

func TestAdminBindingsRejectsInvalidCursor(t *testing.T) {
	query := &auxiliaryQueryHandlerStub{page: &mailapp.AuxiliaryMailPage{Items: []mailapp.AuxiliaryMessageSummary{}, Limit: 20}}
	for _, target := range []string{
		"/v1/admin/bindings?resourceId=9&beforeId=8",
		"/v1/admin/bindings?resourceId=9&beforeReceivedAt=invalid&beforeId=8",
		"/v1/admin/bindings?resourceId=9&beforeReceivedAt=2026-07-12T12%3A00%3A00Z&beforeId=8&offset=1",
		"/v1/admin/bindings?resourceId=9&includeTotal=invalid",
	} {
		recorder := serveAuxiliaryAdminRoute(t, query, true, target)
		require.Equal(t, http.StatusBadRequest, recorder.Code, target)
	}
}

func TestAdminBindingMessageCrossResourceUsesSafeNotFound(t *testing.T) {
	query := &auxiliaryQueryHandlerStub{err: domain.ErrAuxiliaryMessageNotFound}
	recorder := serveAuxiliaryAdminRoute(t, query, true, "/v1/admin/bindings/messages/8?resourceId=9")
	require.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Resource not found.")
}

func serveAuxiliaryAdminRoute(t *testing.T, query mailapp.AuxiliaryMailQueryPort, allowed bool, target string) *httptest.ResponseRecorder {
	return serveAuxiliaryAdminRouteWithAuth(t, query, allowed, true, target)
}

func serveAuxiliaryAdminRouteWithAuth(t *testing.T, query mailapp.AuxiliaryMailQueryPort, allowed, authenticated bool, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	v1 := router.Group("/v1")
	RegisterMailTransportRoutes(v1, &MailTransportModule{AuxiliaryMail: query}, auxiliarySessionFetcher{}, auxiliaryPermissionChecker{allowed: allowed})

	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("X-Request-ID", "test-request")
	if authenticated {
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
