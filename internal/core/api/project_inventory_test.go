package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
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

type repairingInventoryProvider struct {
	projectInventoryProviderStub
	refreshIDs []uint
}

func (p *repairingInventoryProvider) RequestInventoryRefresh(_ context.Context, ids []uint) error {
	p.refreshIDs = append(p.refreshIDs, ids...)
	return nil
}

func TestProjectListSchedulesNewlyEnabledProductInWarmSnapshot(t *testing.T) {
	observed := time.Now().Add(-24 * time.Hour)
	provider := &repairingInventoryProvider{projectInventoryProviderStub: projectInventoryProviderStub{
		totals: &allocapp.ProjectProductInventoryTotals{ProjectID: 10, RefreshedAt: &observed,
			Items: []allocapp.ProductInventoryTotal{{ProductID: 20, ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: 17}}},
	}}
	handler := NewCoreHandler(&CoreModule{ProductInventory: provider})
	summaries := []coreapp.ProjectSummary{{Project: coredomain.Project{ID: 10, Status: coredomain.ProjectStatusListed}, Products: []coredomain.Product{
		{ID: 20, Type: coredomain.ProductTypeMicrosoft, Status: coredomain.ProductStatusEnabled},
		{ID: 21, Type: coredomain.ProductTypeICloud, Status: coredomain.ProductStatusEnabled},
		{ID: 22, Type: coredomain.ProductTypeMicrosoftEnterprise, Status: coredomain.ProductStatusEnabled},
	}}}
	items, err := handler.projectProductInventoryByID(context.Background(), summaries)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.EqualValues(t, 17, items[20].TotalAvailable)
	require.Equal(t, []uint{10}, provider.refreshIDs, "new SKUs must refresh even without orders")
	provider.refreshIDs = nil
	summaries[0].Products[1].Status, summaries[0].Products[2].Status = coredomain.ProductStatusDisabled, coredomain.ProductStatusDisabled
	_, err = handler.projectProductInventoryByID(context.Background(), summaries)
	require.NoError(t, err)
	require.Empty(t, provider.refreshIDs, "disabled products must not cause repeated repairs")
}
