package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coreapp "github.com/donnel666/remail/internal/core/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProjectInventorySnapshotSevenDayBoundary(t *testing.T) {
	now := time.Date(2026, time.November, 9, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		age  time.Duration
		cold bool
		want bool
	}{
		{"hour old", time.Hour, false, true},
		{"weekly snapshot", 6 * 24 * time.Hour, false, true},
		{"exactly seven days", 7 * 24 * time.Hour, false, true},
		{"expired", 7*24*time.Hour + time.Second, false, false},
		{"future timestamp", -time.Second, false, false},
		{"cold placeholder", time.Minute, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := now.Add(-tc.age)
			require.Equal(t, tc.want, projectInventorySnapshotReady(&allocapp.ProjectProductInventoryTotals{
				RefreshedAt: &observed, Cold: tc.cold,
			}, now))
		})
	}
	require.False(t, projectInventorySnapshotReady(nil, now))
	require.False(t, projectInventorySnapshotReady(&allocapp.ProjectProductInventoryTotals{}, now))
}

func TestProjectListKeepsWeeklySnapshotWithoutDetailRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	detail := projectDetailForAPITest()
	repo := newMockProjectRepo()
	repo.summaries = []coreapp.ProjectSummary{{Project: detail.Project, Products: detail.Products}}
	observed := time.Now().UTC().Add(-6 * 24 * time.Hour)
	calls := 0
	h := NewCoreHandler(&CoreModule{
		ProjectUseCase: coreapp.NewProjectUseCase(repo),
		ProductInventory: projectInventoryProviderStub{calls: &calls, totals: &allocapp.ProjectProductInventoryTotals{
			ProjectID: detail.Project.ID, RefreshedAt: &observed,
			Items: []allocapp.ProductInventoryTotal{{ProductID: detail.Products[0].ID, TotalAvailable: 7489322, PublicAvailable: 7489322}},
		}},
	})
	for range 2 { // A reload must get the same cached count without clicking the project.
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/projects?status=listed", nil)
		setAuthContext(ctx, 1, iamdomain.RoleUser)
		h.GetProjects(ctx)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var list ProjectListResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
		require.Len(t, list.Items, 1)
		require.NotNil(t, list.Items[0].Products[0].TotalAvailable)
		require.EqualValues(t, 7489322, *list.Items[0].Products[0].TotalAvailable)
	}
	require.Equal(t, 2, calls, "one batched cache read per page load")
}
