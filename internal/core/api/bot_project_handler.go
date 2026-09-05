package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	allocapi "github.com/donnel666/remail/internal/alloc/api"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	moneyfmt "github.com/donnel666/remail/internal/money"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type BotProjectViewer struct {
	UserID             uint
	PriceDiscountRatio string
}

// BotProjectUserResolver resolves the optional bound remail viewer. false is
// an ordinary unbound caller; resolver failures should abort/write their response.
type BotProjectUserResolver func(*gin.Context) (viewer BotProjectViewer, bound bool)

// BotProjectHandler exposes the ordinary-user project view to authenticated bots.
type BotProjectHandler struct {
	core        *CoreHandler
	resolveUser BotProjectUserResolver
}

func NewBotProjectHandler(module *CoreModule, resolveUser BotProjectUserResolver) *BotProjectHandler {
	return &BotProjectHandler{core: NewCoreHandler(module), resolveUser: resolveUser}
}

func (h *BotProjectHandler) botViewer(c *gin.Context) (BotProjectViewer, bool) {
	if h.resolveUser == nil {
		return BotProjectViewer{PriceDiscountRatio: "1"}, true
	}
	viewer, bound := h.resolveUser(c)
	if c.IsAborted() || c.Writer.Written() {
		return BotProjectViewer{}, false
	}
	if !bound {
		return BotProjectViewer{PriceDiscountRatio: "1"}, true
	}
	if strings.TrimSpace(viewer.PriceDiscountRatio) == "" {
		viewer.PriceDiscountRatio = "1"
	}
	return viewer, true
}

// GetProjects returns the same safe project list used by the normal workbench.
func (h *BotProjectHandler) GetProjects(c *gin.Context) {
	viewer, ok := h.botViewer(c)
	if !ok {
		return
	}
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	if limit > 100 {
		writeBotProjectBadQuery(c)
		return
	}
	filter, ok := projectListFilterFromQuery(c, coreapp.ProjectListScopeVisible, viewer.UserID, false)
	if !ok {
		return
	}
	filter.Scope = coreapp.ProjectListScopeVisible
	filter.Status = coredomain.ProjectStatusListed
	filter.IsAdmin = false

	result, err := h.core.module.ProjectUseCase.List(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	inventory, err := h.core.projectProductInventoryByID(c.Request.Context(), result.Items)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	items := make([]ProjectItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = toProjectItemResponse(result.Items[i], false, viewer.UserID, inventory)
		if err := applyBotEffectiveProjectItemPrices(&items[i], viewer.PriceDiscountRatio); err != nil {
			writeCoreError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, ProjectListResponse{
		Items: items, Total: result.Total, Offset: result.Offset, Limit: result.Limit,
		Facets: toProjectListFacetsResponse(result.Facets),
	})
}

// GetProject returns a listed project through the ordinary-user visibility check.
func (h *BotProjectHandler) GetProject(c *gin.Context) {
	viewer, ok := h.botViewer(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	detail, ok := h.safeProjectDetail(c, projectID, viewer.UserID)
	if !ok {
		return
	}
	totals, err := h.projectInventory(c.Request.Context(), projectID, viewer.UserID)
	if err != nil {
		writeBotInventoryError(c, err)
		return
	}
	inventory := productInventoryByID(totals)
	response := toProjectDetailResponseWithInventory(detail, false, viewer.UserID, inventory)
	if err := applyBotEffectiveProjectDetailPrices(&response, viewer.PriceDiscountRatio); err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// GetProjectInventory returns the existing workbench inventory response.
func (h *BotProjectHandler) GetProjectInventory(c *gin.Context) {
	viewer, ok := h.botViewer(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	if _, ok := h.safeProjectDetail(c, projectID, viewer.UserID); !ok {
		return
	}
	totals, err := h.projectInventory(c.Request.Context(), projectID, viewer.UserID)
	if err != nil {
		writeBotInventoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectInventoryTotalResponse(totals))
}

func (h *BotProjectHandler) safeProjectDetail(c *gin.Context, projectID, userID uint) (*coredomain.ProjectDetail, bool) {
	detail, err := h.core.module.ProjectUseCase.Get(c.Request.Context(), projectID, userID, false)
	if err != nil {
		writeCoreError(c, err)
		return nil, false
	}
	if detail.Project.Status != coredomain.ProjectStatusListed {
		writeCoreError(c, coredomain.ErrProjectNotFound)
		return nil, false
	}
	return detail, true
}

type personalizedProjectInventory interface {
	GetProductInventoryTotals(context.Context, uint, uint) (*allocapp.ProjectProductInventoryTotals, error)
}

func (h *BotProjectHandler) projectInventory(ctx context.Context, projectID, userID uint) (*allocapp.ProjectProductInventoryTotals, error) {
	if h.core.module == nil || h.core.module.ProductInventory == nil {
		return nil, allocdomain.ErrInventoryRefreshInProgress
	}
	var totals *allocapp.ProjectProductInventoryTotals
	var err error
	if userID > 0 {
		if inventory, ok := h.core.module.ProductInventory.(personalizedProjectInventory); ok {
			totals, err = inventory.GetProductInventoryTotals(ctx, projectID, userID)
			if err != nil {
				return nil, err
			}
			if !projectInventorySnapshotReady(totals, time.Now().UTC()) {
				return nil, allocdomain.ErrInventoryRefreshInProgress
			}
			return totals, nil
		}
	}
	snapshots, err := h.core.module.ProductInventory.GetProductInventorySnapshots(ctx, []uint{projectID})
	if err != nil {
		return nil, err
	}
	if totals = snapshots[projectID]; totals == nil {
		return nil, allocdomain.ErrProjectNotAllocatable
	}
	if !projectInventorySnapshotReady(totals, time.Now().UTC()) {
		return nil, allocdomain.ErrInventoryRefreshInProgress
	}
	return totals, nil
}

func productInventoryByID(totals *allocapp.ProjectProductInventoryTotals) map[uint]allocapp.ProductInventoryTotal {
	items := make(map[uint]allocapp.ProductInventoryTotal, len(totals.Items))
	for _, item := range totals.Items {
		items[item.ProductID] = item
	}
	return items
}

func toProjectInventoryTotalResponse(totals *allocapp.ProjectProductInventoryTotals) allocapi.ProjectInventoryTotalResponse {
	products := make([]allocapi.ProjectProductInventoryTotalResponse, len(totals.Items))
	for i, item := range totals.Items {
		suffixes := make([]allocapi.ProjectProductSuffixInventoryResponse, len(item.Suffixes))
		for j, suffix := range item.Suffixes {
			suffixes[j] = allocapi.ProjectProductSuffixInventoryResponse{
				Suffix: suffix.Suffix, TotalAvailable: suffix.TotalAvailable, PublicAvailable: suffix.PublicAvailable,
			}
		}
		products[i] = allocapi.ProjectProductInventoryTotalResponse{
			ProductType: string(item.ProductType), TotalAvailable: item.TotalAvailable, PublicAvailable: item.PublicAvailable,
			CodeAvailable: item.CodeAvailable, CodePublicAvailable: item.CodePublicAvailable,
			PurchaseAvailable: item.PurchaseAvailable, PurchasePublicAvailable: item.PurchasePublicAvailable,
			Suffixes: suffixes,
		}
	}
	return allocapi.ProjectInventoryTotalResponse{
		ProjectID: totals.ProjectID, TotalAvailable: totals.TotalAvailable, Products: products, ObservedAt: totals.RefreshedAt,
	}
}

func writeBotInventoryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, allocdomain.ErrProjectNotAllocatable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Project is not available for allocation.", "requestId": middleware.GetRequestID(c)})
	case errors.Is(err, allocdomain.ErrInventoryRefreshInProgress):
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Inventory is being prepared, please retry.", "requestId": middleware.GetRequestID(c)})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
	}
}

func writeBotProjectBadQuery(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid query parameters.", "requestId": middleware.GetRequestID(c)})
}

func applyBotEffectiveProjectItemPrices(item *ProjectItemResponse, groupRatio string) error {
	for i := range item.Products {
		code, purchase, err := botEffectivePrices(
			item.Products[i].CodePrice,
			item.Products[i].PurchasePrice,
			groupRatio,
			item.Products[i].PriceMultiplier,
		)
		if err != nil {
			return err
		}
		if item.Products[i].Status == string(coredomain.ProductStatusEnabled) && item.Products[i].CodeEnabled {
			item.Products[i].EffectiveCodePrice = code
		}
		if item.Products[i].Status == string(coredomain.ProductStatusEnabled) && item.Products[i].PurchaseEnabled {
			item.Products[i].EffectivePurchasePrice = purchase
		}
	}
	return nil
}

func applyBotEffectiveProjectDetailPrices(detail *ProjectDetailResponse, groupRatio string) error {
	for i := range detail.Products {
		code, purchase, err := botEffectivePrices(
			detail.Products[i].CodePrice,
			detail.Products[i].PurchasePrice,
			groupRatio,
			detail.Products[i].PriceMultiplier,
		)
		if err != nil {
			return err
		}
		if detail.Products[i].Status == string(coredomain.ProductStatusEnabled) && detail.Products[i].CodeEnabled {
			detail.Products[i].EffectiveCodePrice = code
		}
		if detail.Products[i].Status == string(coredomain.ProductStatusEnabled) && detail.Products[i].PurchaseEnabled {
			detail.Products[i].EffectivePurchasePrice = purchase
		}
	}
	return nil
}

func botEffectivePrices(codePrice, purchasePrice, groupRatio, productRatio string) (string, string, error) {
	if strings.TrimSpace(groupRatio) == "" {
		groupRatio = "1"
	}
	if strings.TrimSpace(productRatio) == "" {
		productRatio = "1"
	}
	if strings.TrimSpace(codePrice) == "" {
		codePrice = "0"
	}
	if strings.TrimSpace(purchasePrice) == "" {
		purchasePrice = "0"
	}
	ratio := decimal.NewFromInt(1)
	for _, value := range []string{groupRatio, productRatio} {
		candidate, err := moneyfmt.Parse(value)
		if err != nil || candidate.IsNegative() || candidate.GreaterThan(decimal.NewFromInt(1)) {
			return "", "", errors.New("invalid project price multiplier")
		}
		if candidate.LessThan(ratio) {
			ratio = candidate
		}
	}
	code, err := moneyfmt.Parse(codePrice)
	if err != nil {
		return "", "", err
	}
	purchase, err := moneyfmt.Parse(purchasePrice)
	if err != nil {
		return "", "", err
	}
	return moneyfmt.Format(code.Mul(ratio)), moneyfmt.Format(purchase.Mul(ratio)), nil
}
