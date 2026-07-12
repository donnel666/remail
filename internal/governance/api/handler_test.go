package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type apiTaskRepoStub struct {
	exists      bool
	task        *governanceapp.AdminTaskView
	err         error
	existsCalls int
	lists       int
	gets        int
}

func (s *apiTaskRepoStub) MicrosoftResourceExists(context.Context, uint) (bool, error) {
	s.existsCalls++
	return s.exists, s.err
}

func (s *apiTaskRepoStub) ListForMicrosoftResource(context.Context, governanceapp.AdminTaskListFilter) ([]governanceapp.AdminTaskView, int64, int64, error) {
	s.lists++
	if s.err != nil {
		return nil, 0, 0, s.err
	}
	if s.task == nil {
		return []governanceapp.AdminTaskView{}, 0, 0, nil
	}
	return []governanceapp.AdminTaskView{*s.task}, 1, 0, nil
}

func (s *apiTaskRepoStub) FindByRef(context.Context, governanceapp.AdminTaskRef) (*governanceapp.AdminTaskView, error) {
	s.gets++
	if s.err != nil {
		return nil, s.err
	}
	if s.task == nil {
		return nil, governanceapp.ErrAdminTaskNotFound
	}
	return s.task, nil
}

func TestAdminTaskRouteSecurityMatrix(t *testing.T) {
	for _, path := range []string{
		"/v1/admin/tasks?bizType=microsoft_resource&bizId=61",
		"/v1/admin/tasks/fetch:71",
	} {
		t.Run(path+"/session", func(t *testing.T) {
			repo := &apiTaskRepoStub{exists: true}
			checker := &taskPermissionChecker{allow: true}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			adminTaskTestRouter(repo, checker).ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.Zero(t, repo.existsCalls)
			require.Zero(t, repo.gets)
		})

		t.Run(path+"/permission", func(t *testing.T) {
			repo := &apiTaskRepoStub{exists: true}
			checker := &taskPermissionChecker{allow: false}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
			response := httptest.NewRecorder()
			adminTaskTestRouter(repo, checker).ServeHTTP(response, request)
			require.Equal(t, http.StatusForbidden, response.Code)
			require.Equal(t, "governance:task", checker.resource)
			require.Equal(t, "read", checker.action)
			require.Zero(t, repo.existsCalls)
			require.Zero(t, repo.gets)
		})
	}
}

type taskPermissionChecker struct {
	allow    bool
	resource string
	action   string
}

func (c *taskPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	c.resource = resource
	c.action = action
	return c.allow, nil
}

func TestAdminTaskRoutesRequireGovernancePermissionAndReturnSafeDTO(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
	revision := uint64(4)
	repo := &apiTaskRepoStub{
		exists: true,
		task: &governanceapp.AdminTaskView{
			Ref:                governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceFetch, ID: 71},
			BizType:            governanceapp.AdminTaskBizMicrosoftResource,
			BizID:              61,
			Kind:               governanceapp.AdminTaskKindFetch,
			Status:             governanceapp.AdminTaskStatusRunning,
			Attempts:           1,
			MaxAttempts:        3,
			CredentialRevision: &revision,
			QueuedAt:           now,
			UpdatedAt:          now,
			Progress: &governanceapp.AdminTaskProgress{
				Total:        3,
				Processed:    2,
				Succeeded:    2,
				ReasonCounts: []governanceapp.AdminTaskReasonCount{},
			},
		},
	}
	checker := &taskPermissionChecker{allow: true}
	router := adminTaskTestRouter(repo, checker)

	request := httptest.NewRequest(http.MethodGet, "/v1/admin/tasks?bizType=microsoft_resource&bizId=61", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "governance:task", checker.resource)
	require.Equal(t, "read", checker.action)
	require.Contains(t, response.Body.String(), `"taskId":"fetch:71"`)
	require.Contains(t, response.Body.String(), `"remainingAttempts":2`)
	for _, forbidden := range []string{"claimToken", "dispatchToken", "fencing", "selection_json", "never-return-secret"} {
		require.NotContains(t, response.Body.String(), forbidden)
	}
}

func TestAdminTaskRoutesRejectPermissionBeforeQuery(t *testing.T) {
	repo := &apiTaskRepoStub{exists: true}
	checker := &taskPermissionChecker{allow: false}
	router := adminTaskTestRouter(repo, checker)
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/tasks?bizType=microsoft_resource&bizId=61", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Zero(t, repo.lists)
}

func TestAdminTaskHandlerUsesSafeFlatErrors(t *testing.T) {
	repo := &apiTaskRepoStub{exists: true, err: errors.New("dial tcp secret-host")}
	router := adminTaskTestRouter(repo, &taskPermissionChecker{allow: true})
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/tasks?bizType=microsoft_resource&bizId=61", nil)
	request.Header.Set("X-Request-ID", "req-task-unavailable")
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.JSONEq(t, `{"message":"Service is temporarily unavailable.","requestId":"req-task-unavailable"}`, response.Body.String())
	require.NotContains(t, response.Body.String(), "secret-host")

	invalid := httptest.NewRequest(http.MethodGet, "/v1/admin/tasks/71", nil)
	invalid.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "admin-session"})
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code)
	require.True(t, strings.Contains(invalidResponse.Body.String(), "Invalid request parameters."))
}

func adminTaskTestRouter(repo governanceapp.AdminTaskViewRepository, checker middleware.PermissionChecker) *gin.Engine {
	router := gin.New()
	router.Use(middleware.RequestID())
	v1 := router.Group("/v1")
	module := &Module{Tasks: governanceapp.NewAdminTaskQueryService(repo)}
	RegisterRoutes(v1, module, middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, iamdomain.RoleAdmin, "admin@test.local", true
	}), checker)
	return router
}
