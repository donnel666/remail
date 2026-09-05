package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	allocapi "github.com/donnel666/remail/internal/alloc/api"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type botProjectRepoStub struct {
	coreapp.ProjectRepository
	items       []coreapp.ProjectSummary
	detail      *coredomain.ProjectDetail
	listFilter  coreapp.ProjectListFilter
	findUserID  uint
	findIsAdmin bool
}

func (r *botProjectRepoStub) Count(context.Context, coreapp.ProjectListFilter) (int64, error) {
	return int64(len(r.items)), nil
}

func (r *botProjectRepoStub) List(_ context.Context, filter coreapp.ProjectListFilter, offset, limit int) ([]coreapp.ProjectSummary, error) {
	r.listFilter = filter
	end := min(len(r.items), offset+limit)
	if offset >= end {
		return nil, nil
	}
	return r.items[offset:end], nil
}

func (r *botProjectRepoStub) Facets(context.Context, coreapp.ProjectListFilter) (*coreapp.ProjectListFacets, error) {
	return &coreapp.ProjectListFacets{}, nil
}

func (r *botProjectRepoStub) FindDetail(_ context.Context, _ uint, userID uint, isAdmin bool) (*coredomain.ProjectDetail, error) {
	r.findUserID, r.findIsAdmin = userID, isAdmin
	return r.detail, nil
}

type botPersonalizedInventoryStub struct {
	projectInventoryProviderStub
	viewerUserID uint
	projectID    uint
}

func (s *botPersonalizedInventoryStub) GetProductInventoryTotals(_ context.Context, projectID, viewerUserID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	s.projectID, s.viewerUserID = projectID, viewerUserID
	return s.totals, s.err
}

func TestBotProjectListForcesOrdinaryListedView(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ownerID := uint(42)
	repo := &botProjectRepoStub{items: []coreapp.ProjectSummary{{
		Project: coredomain.Project{
			ID: 7, Name: "Safe", Status: coredomain.ProjectStatusListed, AccessType: coredomain.ProjectAccessPublic,
			ApplicantUserID: &ownerID, ReviewReason: "internal reason",
		},
		Owner:    &coreapp.AdminOwnerSummary{ID: ownerID, Email: "private@example.com"},
		Products: []coredomain.Product{{ID: 70, Type: coredomain.ProductTypeMicrosoft, Status: coredomain.ProductStatusEnabled}},
	}}}
	module := &CoreModule{
		ProjectUseCase: coreapp.NewProjectUseCase(repo),
		ProductInventory: projectInventoryProviderStub{totals: &allocapp.ProjectProductInventoryTotals{
			ProjectID: 7, Items: []allocapp.ProductInventoryTotal{{ProductID: 70}},
		}},
	}
	h := NewBotProjectHandler(module, func(*gin.Context) (BotProjectViewer, bool) {
		return BotProjectViewer{UserID: ownerID, PriceDiscountRatio: "1"}, true
	})
	recorder, c := botProjectContext(http.MethodGet, "/projects?scope=all&status=reviewing")

	h.GetProjects(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, coreapp.ProjectListScopeVisible, repo.listFilter.Scope)
	require.Equal(t, coredomain.ProjectStatusListed, repo.listFilter.Status)
	require.Equal(t, ownerID, repo.listFilter.UserID)
	require.False(t, repo.listFilter.IsAdmin)
	var response ProjectListResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Nil(t, response.Items[0].Owner)
	require.Nil(t, response.Items[0].ApplicantUserID)
	require.Empty(t, response.Items[0].ReviewReason)
}

func TestBotProjectListTreatsUnboundIdentityAsPublicViewer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &botProjectRepoStub{}
	h := NewBotProjectHandler(&CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}, func(*gin.Context) (BotProjectViewer, bool) {
		return BotProjectViewer{}, false
	})
	recorder, c := botProjectContext(http.MethodGet, "/projects")

	h.GetProjects(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Zero(t, repo.listFilter.UserID)
	require.Equal(t, coreapp.ProjectListScopeVisible, repo.listFilter.Scope)
	require.Equal(t, coredomain.ProjectStatusListed, repo.listFilter.Status)
}

func TestBotProjectListCapsWebSocketSizedResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &botProjectRepoStub{}
	h := NewBotProjectHandler(&CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo)}, nil)
	recorder, c := botProjectContext(http.MethodGet, "/projects?limit=101")

	h.GetProjects(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestBotProjectDetailNeverUsesAdminView(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userID := uint(42)
	observedAt := time.Now().UTC()
	repo := &botProjectRepoStub{detail: &coredomain.ProjectDetail{
		Project: coredomain.Project{
			ID: 7, Name: "Safe", Status: coredomain.ProjectStatusListed, AccessType: coredomain.ProjectAccessPrivate,
			ApplicantUserID: &userID, ReviewReason: "internal reason",
		},
		Products: []coredomain.Product{{
			ID: 70, ProjectID: 7, Type: coredomain.ProductTypeMicrosoft, Status: coredomain.ProductStatusEnabled,
			CodeSupplierPrice: "5.000000", MainWeight: 1,
		}},
		MailRules:                []coredomain.MailRule{{ID: 1, ProjectID: 7, RuleType: coredomain.MailRuleSender, Pattern: "secret", Enabled: true}},
		Accesses:                 []coredomain.ProjectAccess{{ID: 1, ProjectID: 7, UserID: userID}},
		MicrosoftSuffixBlacklist: []string{"secret.example"},
	}}
	module := &CoreModule{
		ProjectUseCase: coreapp.NewProjectUseCase(repo),
		ProductInventory: projectInventoryProviderStub{totals: &allocapp.ProjectProductInventoryTotals{
			ProjectID: 7, RefreshedAt: &observedAt, Items: []allocapp.ProductInventoryTotal{{ProductID: 70}},
		}},
	}
	h := NewBotProjectHandler(module, func(*gin.Context) (BotProjectViewer, bool) {
		return BotProjectViewer{UserID: userID, PriceDiscountRatio: "1"}, true
	})
	recorder, c := botProjectContext(http.MethodGet, "/projects/7")
	c.Params = gin.Params{{Key: "projectId", Value: "7"}}

	h.GetProject(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, userID, repo.findUserID)
	require.False(t, repo.findIsAdmin)
	var response ProjectDetailResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Nil(t, response.Project.ApplicantUserID)
	require.Empty(t, response.Project.ReviewReason)
	require.Empty(t, response.MailRules)
	require.Empty(t, response.Accesses)
	require.Empty(t, response.MicrosoftSuffixBlacklist)
	require.Empty(t, response.Products[0].CodeSupplierPrice)
	require.Nil(t, response.Products[0].MainWeight)
}

func TestBotProjectInventoryUsesBoundWorkbenchInventory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	userID := uint(42)
	repo := &botProjectRepoStub{detail: &coredomain.ProjectDetail{Project: coredomain.Project{
		ID: 7, Status: coredomain.ProjectStatusListed, AccessType: coredomain.ProjectAccessPublic,
	}}}
	observedAt := time.Now().UTC()
	inventory := &botPersonalizedInventoryStub{projectInventoryProviderStub: projectInventoryProviderStub{
		totals: &allocapp.ProjectProductInventoryTotals{
			ProjectID: 7, TotalAvailable: 9, RefreshedAt: &observedAt,
			Items: []allocapp.ProductInventoryTotal{{ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: 9, PublicAvailable: 4}},
		},
	}}
	h := NewBotProjectHandler(&CoreModule{ProjectUseCase: coreapp.NewProjectUseCase(repo), ProductInventory: inventory}, func(*gin.Context) (BotProjectViewer, bool) {
		return BotProjectViewer{UserID: userID, PriceDiscountRatio: "1"}, true
	})
	recorder, c := botProjectContext(http.MethodGet, "/projects/7/inventory")
	c.Params = gin.Params{{Key: "projectId", Value: "7"}}

	h.GetProjectInventory(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, userID, inventory.viewerUserID)
	require.Equal(t, uint(7), inventory.projectID)
	var response allocapi.ProjectInventoryTotalResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, int64(9), response.TotalAvailable)
	require.Equal(t, int64(4), response.Products[0].PublicAvailable)
	require.Equal(t, observedAt, *response.ObservedAt)
}

func TestBotProjectColdInventoryIsUnknownInListsAndRetryableInDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	product := coredomain.Product{ID: 70, ProjectID: 7, Type: coredomain.ProductTypeMicrosoft, Status: coredomain.ProductStatusEnabled}
	repo := &botProjectRepoStub{
		items: []coreapp.ProjectSummary{{
			Project:  coredomain.Project{ID: 7, Status: coredomain.ProjectStatusListed, AccessType: coredomain.ProjectAccessPublic},
			Products: []coredomain.Product{product}, ProductCount: 1,
		}},
		detail: &coredomain.ProjectDetail{
			Project:  coredomain.Project{ID: 7, Status: coredomain.ProjectStatusListed, AccessType: coredomain.ProjectAccessPublic},
			Products: []coredomain.Product{product},
		},
	}
	h := NewBotProjectHandler(&CoreModule{
		ProjectUseCase: coreapp.NewProjectUseCase(repo),
		ProductInventory: projectInventoryProviderStub{totals: &allocapp.ProjectProductInventoryTotals{
			ProjectID: 7, Cold: true,
			Items: []allocapp.ProductInventoryTotal{{ProductID: 70, TotalAvailable: 99, PublicAvailable: 99}},
		}},
	}, nil)

	listRecorder, listContext := botProjectContext(http.MethodGet, "/projects")
	h.GetProjects(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	var list ProjectListResponse
	require.NoError(t, json.Unmarshal(listRecorder.Body.Bytes(), &list))
	require.Nil(t, list.Items[0].Products[0].TotalAvailable)
	require.Nil(t, list.Items[0].Products[0].PublicAvailable)

	for _, target := range []string{"/projects/7", "/projects/7/inventory"} {
		recorder, c := botProjectContext(http.MethodGet, target)
		c.Params = gin.Params{{Key: "projectId", Value: "7"}}
		if target == "/projects/7" {
			h.GetProject(c)
		} else {
			h.GetProjectInventory(c)
		}
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, target+": "+recorder.Body.String())
		require.Equal(t, "1", recorder.Header().Get("Retry-After"))
		require.NotContains(t, recorder.Body.String(), "cache")
	}
}

func TestBotProjectStaleInventoryIsUnknownInListsAndRetryableInDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	product := coredomain.Product{ID: 70, ProjectID: 7, Type: coredomain.ProductTypeMicrosoft, Status: coredomain.ProductStatusEnabled}
	stale := time.Now().UTC().Add(-2*allocapp.InventoryRefreshIntervalValue() - time.Second)
	repo := &botProjectRepoStub{
		items: []coreapp.ProjectSummary{{
			Project:  coredomain.Project{ID: 7, Status: coredomain.ProjectStatusListed, AccessType: coredomain.ProjectAccessPublic},
			Products: []coredomain.Product{product}, ProductCount: 1,
		}},
		detail: &coredomain.ProjectDetail{
			Project:  coredomain.Project{ID: 7, Status: coredomain.ProjectStatusListed, AccessType: coredomain.ProjectAccessPublic},
			Products: []coredomain.Product{product},
		},
	}
	h := NewBotProjectHandler(&CoreModule{
		ProjectUseCase: coreapp.NewProjectUseCase(repo),
		ProductInventory: projectInventoryProviderStub{totals: &allocapp.ProjectProductInventoryTotals{
			ProjectID: 7, RefreshedAt: &stale,
			Items: []allocapp.ProductInventoryTotal{{ProductID: 70, TotalAvailable: 99, PublicAvailable: 99}},
		}},
	}, nil)

	listRecorder, listContext := botProjectContext(http.MethodGet, "/projects")
	h.GetProjects(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	var list ProjectListResponse
	require.NoError(t, json.Unmarshal(listRecorder.Body.Bytes(), &list))
	require.Nil(t, list.Items[0].Products[0].TotalAvailable)

	recorder, c := botProjectContext(http.MethodGet, "/projects/7/inventory")
	c.Params = gin.Params{{Key: "projectId", Value: "7"}}
	h.GetProjectInventory(c)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	require.Equal(t, "1", recorder.Header().Get("Retry-After"))
}

func TestBotEffectivePricesUseLowerProductOrUserGroupMultiplier(t *testing.T) {
	code, purchase, err := botEffectivePrices("10.00", "20.00", "0.70", "0.80")
	require.NoError(t, err)
	require.Equal(t, "7.00", code)
	require.Equal(t, "14.00", purchase)
	item := ProjectItemResponse{Products: []ProjectProductSummaryResponse{{
		Status: string(coredomain.ProductStatusEnabled), CodeEnabled: false, PurchaseEnabled: true,
		CodePrice: "10.00", PurchasePrice: "20.00", PriceMultiplier: "0.80",
	}}}
	require.NoError(t, applyBotEffectiveProjectItemPrices(&item, "0.70"))
	require.Empty(t, item.Products[0].EffectiveCodePrice)
	require.Equal(t, "14.00", item.Products[0].EffectivePurchasePrice)
}

func botProjectContext(method, target string) (*httptest.ResponseRecorder, *gin.Context) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, target, nil)
	return recorder, c
}
