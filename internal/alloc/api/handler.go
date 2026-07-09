package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	module *Module
}

func NewHandler(module *Module) *Handler {
	return &Handler{module: module}
}

func (h *Handler) GetAllocations(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	filter := allocapp.AllocationFilter{
		Type:    domain.AllocationType(strings.TrimSpace(c.Query("type"))),
		OrderNo: strings.TrimSpace(c.Query("orderNo")),
		Status:  domain.AllocationStatus(strings.TrimSpace(c.Query("status"))),
		Mailbox: strings.TrimSpace(c.Query("mailbox")),
		Offset:  offset,
		Limit:   limit,
	}
	projectID, ok := parseOptionalUint(c, "projectId")
	if !ok {
		return
	}
	filter.ProjectID = projectID
	resourceID, ok := parseOptionalUint(c, "resourceId")
	if !ok {
		return
	}
	filter.ResourceID = resourceID
	result, err := h.module.UseCase.ListAllocations(c.Request.Context(), filter)
	if err != nil {
		writeAllocError(c, err)
		return
	}
	items := make([]AllocationItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = allocationResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, AllocationListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func (h *Handler) GetAllocation(c *gin.Context) {
	allocationID, ok := parsePathUint(c, "allocationId")
	if !ok {
		return
	}
	allocationType := domain.AllocationType(strings.TrimSpace(c.Query("type")))
	result, err := h.module.UseCase.FindAllocationDetail(c.Request.Context(), allocationType, allocationID)
	if err != nil {
		writeAllocError(c, err)
		return
	}
	c.JSON(http.StatusOK, allocationResponse(*result))
}

func (h *Handler) GetOrderAllocation(c *gin.Context) {
	orderNo := strings.TrimSpace(c.Param("orderNo"))
	result, err := h.module.UseCase.FindAllocationByOrder(c.Request.Context(), orderNo)
	if err != nil {
		writeAllocError(c, err)
		return
	}
	c.JSON(http.StatusOK, allocationResponse(*result))
}

func (h *Handler) GetProjectInventory(c *gin.Context) {
	projectID, ok := parsePathUint(c, "projectId")
	if !ok {
		return
	}
	buyerUserID, ok := parseOptionalUint(c, "buyerUserId")
	if !ok {
		return
	}
	stats, err := h.module.UseCase.GetInventoryStats(c.Request.Context(), projectID, buyerUserID)
	if err != nil {
		writeAllocError(c, err)
		return
	}
	c.JSON(http.StatusOK, ProjectInventoryResponse{
		ProjectID:                  stats.ProjectID,
		Microsoft:                  microsoftInventoryResponse(stats.Microsoft),
		Domain:                     domainInventoryResponse(stats.Domain),
		TotalAvailable:             stats.TotalAvailable,
		ActiveMicrosoftAllocations: stats.ActiveMicrosoftAllocations,
		ActiveDomainAllocations:    stats.ActiveDomainAllocations,
	})
}

func (h *Handler) GetUserProjectInventory(c *gin.Context) {
	projectID, ok := parsePathUint(c, "projectId")
	if !ok {
		return
	}
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return
	}
	stats, err := h.module.UseCase.GetProductInventoryTotals(c.Request.Context(), projectID, userID)
	if err != nil {
		writeAllocError(c, err)
		return
	}
	products := make([]ProjectProductInventoryTotalResponse, len(stats.Items))
	for i := range stats.Items {
		products[i] = ProjectProductInventoryTotalResponse{
			ProductID:      stats.Items[i].ProductID,
			TotalAvailable: stats.Items[i].TotalAvailable,
		}
	}
	c.JSON(http.StatusOK, ProjectInventoryTotalResponse{
		ProjectID:      stats.ProjectID,
		TotalAvailable: stats.TotalAvailable,
		Products:       products,
	})
}

func (h *Handler) GetProjectCandidates(c *gin.Context) {
	projectID, ok := parsePathUint(c, "projectId")
	if !ok {
		return
	}
	candidateType := domain.AllocationType(strings.TrimSpace(c.Query("type")))
	if candidateType != "" && !domain.IsValidAllocationType(candidateType) {
		writeAllocError(c, domain.ErrInvalidAllocationRequest)
		return
	}
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	result, err := h.module.UseCase.ListRoutingCandidates(c.Request.Context(), allocapp.CandidateFilter{
		ProjectID: projectID,
		Type:      candidateType,
		Offset:    offset,
		Limit:     limit,
	})
	if err != nil {
		writeAllocError(c, err)
		return
	}
	items := make([]RoutingCandidateResponse, len(result.Items))
	for i := range result.Items {
		item := result.Items[i]
		items[i] = RoutingCandidateResponse{
			ID:              item.ID,
			Type:            string(item.Type),
			ProjectID:       item.ProjectID,
			ResourceID:      item.ResourceID,
			Address:         item.Address,
			DomainSuffix:    item.DomainSuffix,
			ForSale:         item.ForSale,
			QualityScore:    item.QualityScore,
			Status:          item.Status,
			Bucket:          item.Bucket,
			LastAllocatedAt: item.LastAllocatedAt,
			CreatedAt:       item.CreatedAt,
			UpdatedAt:       item.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, RoutingCandidateListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func (h *Handler) PostProjectCandidatesRefresh(c *gin.Context) {
	projectID, ok := parsePathUint(c, "projectId")
	if !ok {
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return
	}
	result, err := h.module.UseCase.QueueRoutingCandidateRefresh(
		c.Request.Context(),
		projectID,
		operatorUserID,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeAllocError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, CandidateRefreshResponse{
		JobID:     result.JobID,
		ProjectID: result.ProjectID,
		Status:    string(result.Status),
		Created:   result.Created,
		Message:   result.Message,
	})
}

func allocationResponse(item domain.UnifiedAllocation) AllocationItemResponse {
	return AllocationItemResponse{
		Type:       string(item.Type),
		ID:         item.ID,
		OrderNo:    item.OrderNo,
		ProjectID:  item.ProjectID,
		ProductID:  item.ProductID,
		ResourceID: item.ResourceID,
		Mailbox:    item.Mailbox,
		Email:      item.Email,
		Status:     string(item.Status),
		CreatedAt:  item.CreatedAt,
		ReleasedAt: item.ReleasedAt,
	}
}

func microsoftInventoryResponse(stats allocapp.MicrosoftInventoryStats) MicrosoftInventoryResponse {
	return MicrosoftInventoryResponse{
		Enabled:                stats.Enabled,
		MainEnabled:            stats.MainEnabled,
		DotEnabled:             stats.DotEnabled,
		PlusEnabled:            stats.PlusEnabled,
		EligibleResources:      stats.EligibleResources,
		MainAvailable:          stats.MainAvailable,
		ExplicitAliasAvailable: stats.ExplicitAliasAvailable,
		DotCapacity:            stats.DotCapacity,
		ActiveDotAllocations:   stats.ActiveDotAllocations,
		DotAvailable:           stats.DotAvailable,
		PlusDailyLimit:         stats.PlusDailyLimit,
		PlusDailyUsed:          stats.PlusDailyUsed,
		PlusDailyAvailable:     stats.PlusDailyAvailable,
		TotalAvailable:         stats.TotalAvailable,
	}
}

func domainInventoryResponse(stats allocapp.DomainInventoryStats) DomainInventoryResponse {
	return DomainInventoryResponse{
		Enabled:               stats.Enabled,
		EligibleResources:     stats.EligibleResources,
		MailboxDailyLimit:     stats.MailboxDailyLimit,
		MailboxDailyUsed:      stats.MailboxDailyUsed,
		MailboxDailyAvailable: stats.MailboxDailyAvailable,
		TotalAvailable:        stats.TotalAvailable,
	}
}

func parsePagination(c *gin.Context) (int, int, bool) {
	return middleware.ParsePagination(c, middleware.PaginationOptions{
		DefaultLimit: 20,
		MaxLimit:     10000,
	})
}

func parsePathUint(c *gin.Context, name string) (uint, bool) {
	value, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || value == 0 {
		writeBadRequest(c)
		return 0, false
	}
	return uint(value), true
}

func parseOptionalUint(c *gin.Context, name string) (uint, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeBadRequest(c)
		return 0, false
	}
	return uint(value), true
}

func writeBadRequest(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"message":   "Invalid query parameters.",
		"requestId": middleware.GetRequestID(c),
	})
}

func writeAllocError(c *gin.Context, err error) {
	rid := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrAllocationNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": rid})
	case errors.Is(err, domain.ErrInvalidAllocationRequest):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid allocation request.", "requestId": rid})
	case errors.Is(err, domain.ErrProjectNotAllocatable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Project is not available for allocation.", "requestId": rid})
	case errors.Is(err, domain.ErrInsufficientInventory):
		c.JSON(http.StatusConflict, gin.H{"message": "Insufficient inventory.", "requestId": rid})
	case errors.Is(err, domain.ErrAllocationConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Allocation conflict, please retry.", "requestId": rid})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": rid})
	}
}
