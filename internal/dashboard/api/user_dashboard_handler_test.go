package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminUserDashboardViewStub struct {
	userID uint
	from   time.Time
	to     time.Time
}

func (s *adminUserDashboardViewStub) WalletSummary(_ context.Context, userID uint) (float64, float64, error) {
	s.userID = userID
	return 2.48, 0.02, nil
}

func (s *adminUserDashboardViewStub) OrderBuckets(_ context.Context, userID uint, _ string, from, to time.Time) ([]dashboardapp.OrderBucketRow, error) {
	s.userID, s.from, s.to = userID, from, to
	return []dashboardapp.OrderBucketRow{{Orders: 2, CodeOrders: 1, PurchaseOrders: 1}}, nil
}

func (s *adminUserDashboardViewStub) ReceiptBuckets(context.Context, uint, string, time.Time, time.Time) ([]dashboardapp.ReceiptBucketRow, error) {
	return []dashboardapp.ReceiptBucketRow{{Received: 1}}, nil
}

func (s *adminUserDashboardViewStub) PurchaseActivationBuckets(context.Context, uint, string, time.Time, time.Time) ([]dashboardapp.PurchaseActivationBucketRow, error) {
	return []dashboardapp.PurchaseActivationBucketRow{{Activated: 1, TotalSeconds: 27, Timed: 1}}, nil
}

func (*adminUserDashboardViewStub) ProjectCodeRanking(context.Context, uint, time.Time, time.Time) ([]dashboardapp.ProjectCountRow, error) {
	return nil, nil
}

func (*adminUserDashboardViewStub) ProjectSpendBuckets(context.Context, uint, string, time.Time, time.Time) ([]dashboardapp.ProjectSpendRow, error) {
	return nil, nil
}

func (*adminUserDashboardViewStub) RangeAvgReceiptSeconds(context.Context, uint, time.Time, time.Time) (int, error) {
	return 9, nil
}

func (*adminUserDashboardViewStub) Leaderboard(context.Context, *time.Time, int) ([]dashboardapp.LeaderRow, error) {
	return nil, nil
}

func (*adminUserDashboardViewStub) UserStanding(context.Context, uint, *time.Time) (dashboardapp.Standing, error) {
	return dashboardapp.Standing{}, nil
}

type adminUserDashboardSessionFetcher struct{}

func (adminUserDashboardSessionFetcher) FetchSession(_ context.Context, sessionID string) (uint, iamdomain.Role, string, bool) {
	return 1, iamdomain.RoleAdmin, "admin@example.com", sessionID == "valid"
}

type adminUserDashboardPermissionChecker map[string]bool

func (c adminUserDashboardPermissionChecker) Check(_ context.Context, _ uint, _ iamdomain.Role, resource, action string) (bool, error) {
	return c[resource+":"+action], nil
}

func TestAdminUserDashboardRouteAuthorizationAndTargeting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	all := adminUserDashboardPermissionChecker{
		"iam:user:read":       true,
		"billing:wallet:read": true,
	}

	t.Run("requires authentication", func(t *testing.T) {
		response := adminUserDashboardRequest(t, "/v1/admin/users/42/dashboard", nil, all, false)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	})

	t.Run("requires user read", func(t *testing.T) {
		response := adminUserDashboardRequest(t, "/v1/admin/users/42/dashboard", &adminUserDashboardViewStub{}, adminUserDashboardPermissionChecker{"billing:wallet:read": true}, true)
		require.Equal(t, http.StatusForbidden, response.Code)
	})

	t.Run("requires wallet read", func(t *testing.T) {
		response := adminUserDashboardRequest(t, "/v1/admin/users/42/dashboard", &adminUserDashboardViewStub{}, adminUserDashboardPermissionChecker{"iam:user:read": true}, true)
		require.Equal(t, http.StatusForbidden, response.Code)
	})

	t.Run("rejects invalid user id", func(t *testing.T) {
		response := adminUserDashboardRequest(t, "/v1/admin/users/invalid/dashboard", &adminUserDashboardViewStub{}, all, true)
		require.Equal(t, http.StatusBadRequest, response.Code)
	})

	t.Run("queries the selected user and range", func(t *testing.T) {
		view := &adminUserDashboardViewStub{}
		response := adminUserDashboardRequest(t, "/v1/admin/users/42/dashboard?createdFrom=2026-07-01T00%3A00%3A00Z&createdTo=2026-07-02T00%3A00%3A00Z", view, all, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.EqualValues(t, 42, view.userID)
		require.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), view.from)
		require.Equal(t, time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), view.to)
		require.JSONEq(t, `{"walletBalance":2.48,"historicalSpend":0.02,"codeSuccessRate":100,"averageCodeReceiptSeconds":9,"purchaseActivationSuccessRate":100,"averagePurchaseActivationSeconds":27}`, response.Body.String())
	})
}

func adminUserDashboardRequest(t *testing.T, target string, view *adminUserDashboardViewStub, checker middleware.PermissionChecker, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	if view == nil {
		view = &adminUserDashboardViewStub{}
	}
	router := gin.New()
	router.Use(middleware.RequestID())
	RegisterAdminRoutes(router.Group("/v1"), &Module{Query: dashboardapp.NewQueryService(view)}, adminUserDashboardSessionFetcher{}, checker)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if authenticated {
		request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "valid"})
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
