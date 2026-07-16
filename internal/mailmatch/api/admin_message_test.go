package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminMessageRoutesRequireSessionAndReadPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{
		"/v1/admin/messages?resourceId=100",
		"/v1/admin/messages/7?resourceId=100",
	} {
		t.Run(path+"/session", func(t *testing.T) {
			router, _, _ := newAdminMessageTestRouter(false)
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
		})

		t.Run(path+"/permission", func(t *testing.T) {
			router, _, checker := newAdminMessageTestRouter(false)
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusForbidden, response.Code)
			require.Equal(t, "mailmatch:message", checker.resource)
			require.Equal(t, "read", checker.action)
		})
	}
}

func TestAdminMessageListReturnsSummaryOnlyAndDetailAuditsBodyRead(t *testing.T) {
	router, repo, checker := newAdminMessageTestRouter(true)
	listResponse := performAdminMessageGET(router, "/v1/admin/messages?resourceId=100&type=domain&offset=20&limit=10&search=body")
	require.Equal(t, http.StatusOK, listResponse.Code, listResponse.Body.String())
	require.Equal(t, "mailmatch:message", checker.resource)
	require.Equal(t, "read", checker.action)

	var list map[string]any
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &list))
	items, ok := list["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	require.Equal(t, float64(37), list["total"])
	require.Equal(t, mailmatchdomain.ResourceTypeDomain, repo.listQuery.ResourceType)
	require.Equal(t, uint(100), repo.listQuery.ResourceID)
	require.Equal(t, "body", repo.listQuery.Search)
	require.Equal(t, 20, repo.listQuery.Offset)
	require.Equal(t, 10, repo.listQuery.Limit)
	require.False(t, repo.listQuery.SkipTotal)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	for _, forbiddenKey := range []string{"body", "matchDiagnostic", "rawBody", "objectKey", "providerPayload"} {
		require.NotContains(t, item, forbiddenKey)
	}
	require.NotContains(t, strings.ToLower(listResponse.Body.String()), "body-sensitive-canary")

	detailResponse := performAdminMessageGET(router, "/v1/admin/messages/7?resourceId=100")
	require.Equal(t, http.StatusOK, detailResponse.Code, detailResponse.Body.String())
	var detail map[string]any
	require.NoError(t, json.Unmarshal(detailResponse.Body.Bytes(), &detail))
	require.Equal(t, "body-sensitive-canary", detail["body"])
	require.Equal(t, "Message did not match any active order service.", detail["matchDiagnostic"])
	require.Equal(t, float64(7), detail["id"])
	require.Equal(t, "main", detail["mailbox"])
	require.NotContains(t, detail, "objectKey")
	require.NotContains(t, detail, "rawEnvelope")
	require.NotContains(t, detail, "token")
	require.NotNil(t, repo.readLog)
	require.Equal(t, "mailmatch.admin_message.body.read", repo.readLog.OperationType)
	require.Equal(t, "7", repo.readLog.ResourceID)
	serializedLog := strings.ToLower(fmt.Sprintf("%+v", repo.readLog))
	require.NotContains(t, serializedLog, "body-sensitive-canary")
	require.NotContains(t, serializedLog, "123456")
	require.NotContains(t, serializedLog, "main@example.com")
}

func TestAdminMessageListAcceptsStableCursorAndSkipsRepeatedTotal(t *testing.T) {
	router, repo, _ := newAdminMessageTestRouter(true)
	repo.listHasMore = true
	response := performAdminMessageGET(router, "/v1/admin/messages?resourceId=100&type=domain&beforeReceivedAt=2026-07-12T09%3A00%3A00Z&beforeId=7&includeTotal=false&limit=10")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NotNil(t, repo.listQuery.BeforeReceivedAt)
	require.Equal(t, time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC), *repo.listQuery.BeforeReceivedAt)
	require.Equal(t, uint(7), repo.listQuery.BeforeID)
	require.True(t, repo.listQuery.SkipTotal)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotContains(t, body, "total")
	require.Equal(t, true, body["hasMore"])
	require.Equal(t, "2026-07-12T09:00:00Z", body["nextBeforeReceivedAt"])
	require.Equal(t, float64(7), body["nextBeforeId"])
}

func TestAdminMessageRoutesUseSafeNotFoundAndValidatePagination(t *testing.T) {
	router, _, _ := newAdminMessageTestRouter(true)

	response := performAdminMessageGET(router, "/v1/admin/messages?resourceId=999")
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Contains(t, response.Body.String(), "Resource not found.")

	response = performAdminMessageGET(router, "/v1/admin/messages/7?resourceId=101")
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Contains(t, response.Body.String(), "Message not found.")

	response = performAdminMessageGET(router, "/v1/admin/messages?resourceId=100&limit=101")
	require.Equal(t, http.StatusBadRequest, response.Code)
	response = performAdminMessageGET(router, "/v1/admin/messages?resourceId=100&offset=-1")
	require.Equal(t, http.StatusBadRequest, response.Code)
	response = performAdminMessageGET(router, "/v1/admin/messages?resourceId=100&beforeId=7")
	require.Equal(t, http.StatusBadRequest, response.Code)
	response = performAdminMessageGET(router, "/v1/admin/messages?resourceId=100&beforeReceivedAt=2026-07-12T09%3A00%3A00Z&beforeId=7&offset=1")
	require.Equal(t, http.StatusBadRequest, response.Code)
	response = performAdminMessageGET(router, "/v1/admin/messages?resourceId=100&includeTotal=invalid")
	require.Equal(t, http.StatusBadRequest, response.Code)
}

func newAdminMessageTestRouter(allowed bool) (*gin.Engine, *adminMessageRepoStub, *adminMessagePermissionChecker) {
	repo := &adminMessageRepoStub{}
	checker := &adminMessagePermissionChecker{allowed: allowed}
	module := &Module{AdminMessages: mailmatchapp.NewAdminMessageUseCase(repo)}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterAdminRoutes(
		router.Group("/v1"),
		module,
		middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
			return 1, iamdomain.RoleAdmin, "admin@test.local", true
		}),
		checker,
	)
	return router, repo, checker
}

func performAdminMessageGET(router *gin.Engine, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

type adminMessagePermissionChecker struct {
	allowed  bool
	resource string
	action   string
}

func (c *adminMessagePermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource string, action string) (bool, error) {
	c.resource = resource
	c.action = action
	return c.allowed, nil
}

type adminMessageRepoStub struct {
	listQuery   mailmatchapp.AdminMessageListQuery
	listHasMore bool
	readLog     *governancedomain.OperationLog
}

func (*adminMessageRepoStub) AdminMessageResourceExists(_ context.Context, resourceID uint, _ mailmatchdomain.ResourceType) (bool, error) {
	return resourceID == 100, nil
}

func (r *adminMessageRepoStub) ListAdminMessageSummaries(_ context.Context, query mailmatchapp.AdminMessageListQuery) ([]mailmatchapp.AdminMessageSummary, int64, bool, error) {
	r.listQuery = query
	code := "123456"
	orderNo := "OR-ADMIN-MESSAGE"
	return []mailmatchapp.AdminMessageSummary{{
		ID:               7,
		Mailbox:          "main",
		Recipient:        "main@example.com",
		Sender:           "sender@example.net",
		Subject:          "Summary subject",
		Preview:          "Bounded safe preview",
		Status:           mailmatchdomain.MessageStatusMatched,
		VerificationCode: &code,
		OrderNo:          &orderNo,
		ReceivedAt:       time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC),
	}}, 37, r.listHasMore, nil
}

func (r *adminMessageRepoStub) FindAdminMessageDetailWithLog(_ context.Context, resourceID uint, _ mailmatchdomain.ResourceType, messageID uint, log *governancedomain.OperationLog) (*mailmatchapp.AdminMessageDetail, error) {
	if resourceID != 100 || messageID != 7 {
		return nil, mailmatchdomain.ErrMessageNotFound
	}
	copyLog := *log
	r.readLog = &copyLog
	code := "123456"
	orderNo := "OR-ADMIN-MESSAGE"
	diagnostic := "Message did not match any active order service."
	return &mailmatchapp.AdminMessageDetail{
		AdminMessageSummary: mailmatchapp.AdminMessageSummary{
			ID:               7,
			Mailbox:          "main",
			Recipient:        "main@example.com",
			Sender:           "sender@example.net",
			Subject:          "Detail subject",
			Preview:          "Bounded safe preview",
			Status:           mailmatchdomain.MessageStatusMatched,
			VerificationCode: &code,
			OrderNo:          &orderNo,
			ReceivedAt:       time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC),
		},
		Body:            "body-sensitive-canary",
		MatchDiagnostic: &diagnostic,
	}, nil
}
