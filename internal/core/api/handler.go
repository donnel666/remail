package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

// MaxImportBytes limits TXT import content to prevent memory exhaustion.
const MaxImportBytes = 100 * 1024 * 1024 // 100 MB

// MaxProjectLogoBytes limits project logo uploads to small web-safe images.
const MaxProjectLogoBytes = 2 * 1024 * 1024 // 2 MB

// MaxResourceValidationRequestBytes bounds the explicit-ID selection before
// JSON decoding. Ten thousand numeric IDs fit comfortably within this limit.
const MaxResourceValidationRequestBytes = 1024 * 1024 // 1 MB

// CoreHandler holds the Core HTTP handlers.
type CoreHandler struct {
	module            *CoreModule
	permissionChecker middleware.PermissionChecker
}

// NewCoreHandler creates a new Core handler.
func NewCoreHandler(module *CoreModule, checkers ...middleware.PermissionChecker) *CoreHandler {
	var checker middleware.PermissionChecker
	if len(checkers) > 0 {
		checker = checkers[0]
	}
	return &CoreHandler{module: module, permissionChecker: checker}
}

// --- Resources ---

// GET /v1/resources
func (h *CoreHandler) GetResources(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	scope := c.DefaultQuery("scope", "owned")
	filter, ok := resourceListFilterFromQuery(c)
	if !ok {
		return
	}
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	afterID, ok := parseOptionalUintQuery(c, "afterId")
	if !ok {
		return
	}

	// Non-admin users can only see their own resources
	if scope == "all" {
		role, ok := middleware.GetCurrentRole(c)
		if !ok || !role.HasAdminAccess() {
			scope = "owned"
		}
	}

	result, err := h.module.ResourceUseCase.List(c.Request.Context(), userID, scope, filter, offset, limit, afterID)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	items := make([]ResourceItemResponse, len(result.Items))
	for i, item := range result.Items {
		items[i] = ResourceItemResponse{
			ID:              item.ID,
			Type:            string(item.Type),
			OwnerID:         item.OwnerID,
			Status:          item.Status,
			ForSale:         item.ForSale,
			LongLived:       item.LongLived,
			GraphAvailable:  item.GraphAvailable,
			LastSafeError:   item.LastSafeError,
			Email:           item.Email,
			Domain:          item.Domain,
			DomainTLD:       item.DomainTLD,
			MailServerID:    item.MailServerID,
			Purpose:         item.Purpose,
			MailboxCount:    item.MailboxCount,
			LastAllocatedAt: item.LastAllocatedAt,
			CreatedAt:       item.CreatedAt,
			UpdatedAt:       item.UpdatedAt,
		}
	}

	c.JSON(http.StatusOK, ResourceListResponse{
		Items:       items,
		Total:       result.Total,
		Offset:      result.Offset,
		Limit:       result.Limit,
		NextAfterID: result.NextAfterID,
		Facets:      toResourceListFacetsResponse(result.Facets),
	})
}

// GET /v1/resources/:resourceId
func (h *CoreHandler) GetResourceDetail(c *gin.Context) {
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}

	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	detail, err := h.module.ResourceUseCase.GetDetail(c.Request.Context(), resourceID, userID)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	// Convert app-layer DTO to API-layer DTO to preserve layering
	switch d := detail.(type) {
	case *coreapp.MicrosoftResourceDetail:
		c.JSON(http.StatusOK, MicrosoftResourceDetailResponse{
			ID:              d.ID,
			EmailAddress:    d.EmailAddress,
			ForSale:         d.ForSale,
			LongLived:       d.LongLived,
			GraphAvailable:  d.GraphAvailable,
			Status:          d.Status,
			QualityScore:    d.QualityScore,
			LastSafeError:   d.LastSafeError,
			LastAllocatedAt: d.LastAllocatedAt,
			CreatedAt:       d.CreatedAt,
		})
	case *coreapp.DomainResourceDetail:
		c.JSON(http.StatusOK, DomainResourceDetailResponse{
			ID:              d.ID,
			Domain:          d.Domain,
			MailServerID:    d.MailServerID,
			Purpose:         d.Purpose,
			Status:          d.Status,
			LastSafeError:   d.LastSafeError,
			LastAllocatedAt: d.LastAllocatedAt,
			CreatedAt:       d.CreatedAt,
		})
	default:
		writeCoreError(c, coredomain.ErrInvalidResourceType)
	}
}

// DELETE /v1/resources/:resourceId
func (h *CoreHandler) DeleteResource(c *gin.Context) {
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}

	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	if err := h.module.ResourceUseCase.DeletePrivateResource(
		c.Request.Context(),
		resourceID,
		userID,
		middleware.GetRequestID(c),
		c.FullPath(),
	); err != nil {
		writeCoreError(c, err)
		return
	}

	c.AbortWithStatus(http.StatusNoContent)
}

// POST /v1/resources/delete
func (h *CoreHandler) PostResourceDeleteBatch(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	var req DeleteResourcesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if fields := validateBulkSelectionRequest(req.Selection); len(fields) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    fields,
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	result, err := h.module.ResourceUseCase.DeletePrivateResourcesBatch(
		c.Request.Context(),
		toAppBulkSelection(req.Selection),
		userID,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusOK, DeleteResourcesResponse{
		Requested:          result.Requested,
		Deleted:            result.Deleted,
		DeletedResourceIDs: result.DeletedResourceIDs,
	})
}

// POST /v1/resources/imports
func (h *CoreHandler) PostResourceImport(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	// Limit request body size to prevent memory exhaustion
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxImportBytes)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid import file.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	defer file.Close()

	longLived, err := strconv.ParseBool(c.PostForm("longLived"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid longLived value.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	errorStrategy, ok := coredomain.NormalizeImportErrorStrategy(c.PostForm("errorStrategy"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid errorStrategy value.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid import file.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	result, err := h.module.ImportUseCase.AcceptMicrosoftTXTFile(c.Request.Context(), userID, header.Filename, content, longLived, errorStrategy, middleware.GetRequestID(c))
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusAccepted, ImportResponse{ImportID: result.ImportID, Imported: result.Imported})
}

// GET /v1/resources/imports/:importId
func (h *CoreHandler) GetResourceImport(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	rawID := c.Param("importId")
	parsed, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || parsed == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid import id.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	result, err := h.module.ImportUseCase.GetImportStatus(c.Request.Context(), userID, uint(parsed))
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusOK, ImportStatusResponse{
		ImportID:      result.ImportID,
		Status:        result.Status,
		Imported:      result.Imported,
		LastSafeError: result.LastSafeError,
		CreatedAt:     result.CreatedAt,
		UpdatedAt:     result.UpdatedAt,
	})
}

// POST /v1/resources/:resourceId/publish
func (h *CoreHandler) PostResourcePublish(c *gin.Context) {
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}

	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	if !requireSupplier(c) {
		return
	}

	detail, err := h.module.ResourceUseCase.PublishResourceForSale(
		c.Request.Context(),
		resourceID,
		userID,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	switch d := detail.(type) {
	case *coreapp.MicrosoftResourceDetail:
		c.JSON(http.StatusOK, MicrosoftResourceDetailResponse{
			ID:              d.ID,
			EmailAddress:    d.EmailAddress,
			ForSale:         d.ForSale,
			LongLived:       d.LongLived,
			GraphAvailable:  d.GraphAvailable,
			Status:          d.Status,
			QualityScore:    d.QualityScore,
			LastSafeError:   d.LastSafeError,
			LastAllocatedAt: d.LastAllocatedAt,
			CreatedAt:       d.CreatedAt,
		})
	case *coreapp.DomainResourceDetail:
		c.JSON(http.StatusOK, DomainResourceDetailResponse{
			ID:              d.ID,
			Domain:          d.Domain,
			MailServerID:    d.MailServerID,
			Purpose:         d.Purpose,
			Status:          d.Status,
			LastAllocatedAt: d.LastAllocatedAt,
			CreatedAt:       d.CreatedAt,
		})
	default:
		writeCoreError(c, coredomain.ErrInvalidResourceType)
	}
}

// POST /v1/resources/publish
func (h *CoreHandler) PostResourcePublishBatch(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	if !requireSupplier(c) {
		return
	}

	var req PublishResourcesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if fields := validateBulkSelectionRequest(req.Selection); len(fields) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    fields,
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	result, err := h.module.ResourceUseCase.PublishResourcesForSaleBatch(
		c.Request.Context(),
		toAppBulkSelection(req.Selection),
		userID,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusOK, PublishResourcesResponse{
		Requested:            result.Requested,
		Published:            result.Published,
		PublishedResourceIDs: result.PublishedResourceIDs,
	})
}

// POST /v1/resources/:resourceId/validate
func (h *CoreHandler) PostResourceValidate(c *gin.Context) {
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	role, _ := middleware.GetCurrentRole(c)
	result, err := h.module.ValidationUseCase.Create(
		c.Request.Context(),
		resourceID,
		userID,
		role.HasAdminAccess(),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusAccepted, ResourceValidationsResponse{Requested: result.Requested, Queued: result.Queued})
}

// POST /v1/resources/validations
func (h *CoreHandler) PostResourceValidations(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxResourceValidationRequestBytes)
	var req ValidateResourcesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if fields := validateBulkSelectionRequest(req.Selection); len(fields) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    fields,
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if req.Selection.Mode == "ids" && len(req.Selection.ResourceIDs) > coreapp.ResourceValidationMaxExplicitIDs {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "Invalid request body.",
			"fields": map[string]string{
				"selection.resourceIds": "At most 10000 resource IDs are allowed; use filter mode for larger selections.",
			},
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	role, _ := middleware.GetCurrentRole(c)
	result, err := h.module.ValidationUseCase.CreateBatch(
		c.Request.Context(),
		toAppBulkSelection(req.Selection),
		userID,
		role.HasAdminAccess(),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusAccepted, ResourceValidationsResponse{
		Requested: result.Requested,
		Queued:    result.Queued,
	})
}

// --- Projects ---

// GET /v1/projects
func (h *CoreHandler) GetProjects(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}

	role, _ := middleware.GetCurrentRole(c)
	scope := coreapp.ProjectListScope(c.DefaultQuery("scope", "visible"))
	isAdmin := false
	if role.HasAdminAccess() && scope == coreapp.ProjectListScopeAll {
		allowed, handled := h.checkProjectReadPermission(c, userID, role)
		if handled {
			return
		}
		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{
				"message":   "Permission denied.",
				"requestId": middleware.GetRequestID(c),
			})
			return
		}
		isAdmin = true
	}
	filter, ok := projectListFilterFromQuery(c, scope, userID, isAdmin)
	if !ok {
		return
	}

	result, err := h.module.ProjectUseCase.List(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	items := make([]ProjectItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = toProjectItemResponse(result.Items[i], isAdmin, userID)
	}
	c.JSON(http.StatusOK, ProjectListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
		Facets: toProjectListFacetsResponse(result.Facets),
	})
}

// POST /v1/projects
func (h *CoreHandler) PostProject(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	var req CreateProjectApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	detail, err := h.module.ProjectUseCase.Apply(
		c.Request.Context(),
		userID,
		toAppProjectRequest(req),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusCreated, toProjectDetailResponse(detail, false, userID))
}

// GET /v1/projects/:projectId
func (h *CoreHandler) GetProject(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}

	role, _ := middleware.GetCurrentRole(c)
	includeInternal := false
	if role.HasAdminAccess() {
		allowed, handled := h.checkProjectReadPermission(c, userID, role)
		if handled {
			return
		}
		includeInternal = allowed
	}
	detail, err := h.module.ProjectUseCase.Get(c.Request.Context(), projectID, userID, includeInternal)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusOK, toProjectDetailResponse(detail, includeInternal, userID))
}

// POST /v1/projects/:projectId/resubmit
func (h *CoreHandler) PostProjectResubmit(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}

	var req CreateProjectApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	detail, err := h.module.ProjectUseCase.Resubmit(
		c.Request.Context(),
		userID,
		projectID,
		toAppProjectRequest(req),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusOK, toProjectDetailResponse(detail, false, userID))
}

// POST /v1/admin/projects
func (h *CoreHandler) PostAdminProject(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	var req AdminCreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	detail, err := h.module.ProjectUseCase.AdminCreateListed(
		c.Request.Context(),
		userID,
		toAppProjectRequest(req),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusCreated, toProjectDetailResponse(detail, true, userID))
}

// PUT /v1/admin/projects/:projectId
func (h *CoreHandler) PutAdminProject(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}

	var req AdminCreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	detail, err := h.module.ProjectUseCase.AdminUpdate(
		c.Request.Context(),
		userID,
		projectID,
		toAppProjectRequest(req),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusOK, toProjectDetailResponse(detail, true, userID))
}

// POST /v1/admin/projects/:projectId/approve
func (h *CoreHandler) PostAdminProjectApprove(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	if c.Request.ContentLength == 0 {
		detail, err := h.module.ProjectUseCase.AdminApprove(c.Request.Context(), userID, projectID, middleware.GetRequestID(c), c.FullPath())
		if err != nil {
			writeCoreError(c, err)
			return
		}
		c.JSON(http.StatusOK, toProjectDetailResponse(detail, true, userID))
		return
	}

	var req AdminCreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	detail, err := h.module.ProjectUseCase.AdminApproveWithConfig(
		c.Request.Context(),
		userID,
		projectID,
		toAppProjectRequest(req),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectDetailResponse(detail, true, userID))
}

// POST /v1/admin/projects/:projectId/reject
func (h *CoreHandler) PostAdminProjectReject(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}

	var req AdminRejectProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	detail, err := h.module.ProjectUseCase.AdminReject(
		c.Request.Context(),
		userID,
		projectID,
		req.ReviewReason,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectDetailResponse(detail, true, userID))
}

// POST /v1/admin/projects/:projectId/duplicate
func (h *CoreHandler) PostAdminProjectDuplicate(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}

	var req AdminRejectProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	detail, err := h.module.ProjectUseCase.AdminDuplicate(
		c.Request.Context(),
		userID,
		projectID,
		req.ReviewReason,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectDetailResponse(detail, true, userID))
}

// POST /v1/admin/projects/:projectId/relist
func (h *CoreHandler) PostAdminProjectRelist(c *gin.Context) {
	h.adminProjectTransition(c, func(ctxUserID, projectID uint) (*coredomain.ProjectDetail, error) {
		return h.module.ProjectUseCase.AdminRelist(c.Request.Context(), ctxUserID, projectID, middleware.GetRequestID(c), c.FullPath())
	})
}

// POST /v1/admin/projects/:projectId/delist
func (h *CoreHandler) PostAdminProjectDelist(c *gin.Context) {
	h.adminProjectTransition(c, func(ctxUserID, projectID uint) (*coredomain.ProjectDetail, error) {
		return h.module.ProjectUseCase.AdminDelist(c.Request.Context(), ctxUserID, projectID, middleware.GetRequestID(c), c.FullPath())
	})
}

// DELETE /v1/admin/projects/:projectId
func (h *CoreHandler) DeleteAdminProject(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	if err := h.module.ProjectUseCase.AdminDelete(c.Request.Context(), userID, projectID, middleware.GetRequestID(c), c.FullPath()); err != nil {
		writeCoreError(c, err)
		return
	}
	c.AbortWithStatus(http.StatusNoContent)
}

// GET /v1/admin/projects/:projectId/access
func (h *CoreHandler) GetAdminProjectAccess(c *gin.Context) {
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	accesses, err := h.module.ProjectUseCase.AdminListAccesses(c.Request.Context(), projectID)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, ProjectAccessListResponse{
		Items: toProjectAccessResponses(accesses),
		Total: len(accesses),
	})
}

// POST /v1/admin/projects/:projectId/access
func (h *CoreHandler) PostAdminProjectAccess(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	var req GrantProjectAccessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	access, err := h.module.ProjectUseCase.AdminGrantAccess(
		c.Request.Context(),
		userID,
		projectID,
		req.UserID,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectAccessResponse(*access))
}

// DELETE /v1/admin/projects/:projectId/access/:userId
func (h *CoreHandler) DeleteAdminProjectAccess(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	targetUserID, ok := parseUintParam(c, "userId", "Invalid user ID.")
	if !ok {
		return
	}
	if err := h.module.ProjectUseCase.AdminRevokeAccess(
		c.Request.Context(),
		operatorUserID,
		projectID,
		targetUserID,
		middleware.GetRequestID(c),
		c.FullPath(),
	); err != nil {
		writeCoreError(c, err)
		return
	}
	c.AbortWithStatus(http.StatusNoContent)
}

// POST /v1/admin/projects/logos
func (h *CoreHandler) PostAdminProjectLogo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxProjectLogoBytes)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid project logo.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid project logo.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	logoURL, err := h.module.ProjectAssets.SaveLogo(c.Request.Context(), header.Filename, content)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusCreated, ProjectLogoUploadResponse{LogoURL: logoURL})
}

// GET /v1/projects/logos/:logoKey
func (h *CoreHandler) GetProjectLogo(c *gin.Context) {
	logo, err := h.module.ProjectAssets.ReadLogo(c.Request.Context(), c.Param("logoKey"))
	if err != nil {
		writeCoreError(c, err)
		return
	}
	contentType := strings.TrimSpace(logo.ContentType)
	if contentType == "" {
		contentType = http.DetectContentType(logo.Content)
	}
	c.Data(http.StatusOK, contentType, logo.Content)
}

func (h *CoreHandler) adminProjectTransition(
	c *gin.Context,
	action func(userID, projectID uint) (*coredomain.ProjectDetail, error),
) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	projectID, ok := parseUintParam(c, "projectId", "Invalid project ID.")
	if !ok {
		return
	}
	detail, err := action(userID, projectID)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectDetailResponse(detail, true, userID))
}

// POST /v1/admin/projects/relist
func (h *CoreHandler) PostAdminProjectsRelist(c *gin.Context) {
	h.adminProjectBulkCommand(c, func(userID uint, selection coreapp.ProjectBulkSelection, _ string) (*coreapp.ProjectBulkResult, error) {
		return h.module.ProjectUseCase.AdminBulkRelist(c.Request.Context(), userID, selection, middleware.GetRequestID(c), c.FullPath())
	})
}

// POST /v1/admin/projects/delist
func (h *CoreHandler) PostAdminProjectsDelist(c *gin.Context) {
	h.adminProjectBulkCommand(c, func(userID uint, selection coreapp.ProjectBulkSelection, _ string) (*coreapp.ProjectBulkResult, error) {
		return h.module.ProjectUseCase.AdminBulkDelist(c.Request.Context(), userID, selection, middleware.GetRequestID(c), c.FullPath())
	})
}

// POST /v1/admin/projects/reject
func (h *CoreHandler) PostAdminProjectsReject(c *gin.Context) {
	h.adminProjectBulkCommand(c, func(userID uint, selection coreapp.ProjectBulkSelection, reviewReason string) (*coreapp.ProjectBulkResult, error) {
		return h.module.ProjectUseCase.AdminBulkReject(c.Request.Context(), userID, selection, reviewReason, middleware.GetRequestID(c), c.FullPath())
	})
}

// POST /v1/admin/projects/delete
func (h *CoreHandler) PostAdminProjectsDelete(c *gin.Context) {
	h.adminProjectBulkCommand(c, func(userID uint, selection coreapp.ProjectBulkSelection, _ string) (*coreapp.ProjectBulkResult, error) {
		return h.module.ProjectUseCase.AdminBulkDelete(c.Request.Context(), userID, selection, middleware.GetRequestID(c), c.FullPath())
	})
}

func (h *CoreHandler) adminProjectBulkCommand(
	c *gin.Context,
	action func(userID uint, selection coreapp.ProjectBulkSelection, reviewReason string) (*coreapp.ProjectBulkResult, error),
) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	var req ProjectBulkCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if fields := validateProjectBulkSelectionRequest(req.Selection); len(fields) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    fields,
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	selection := toAppProjectBulkSelection(req.Selection)
	result, err := action(userID, selection, req.ReviewReason)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, ProjectBulkCommandResponse{Affected: result.Affected})
}

// --- Mail Servers ---

// requireCurrentUserID verifies the request is authenticated and returns the current user ID.
func requireCurrentUserID(c *gin.Context) (uint, bool) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return userID, true
}

// requireSupplier verifies the user has at least supplier role.
func requireSupplier(c *gin.Context) bool {
	role, ok := middleware.GetCurrentRole(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return false
	}
	if !role.HasSupplierAccess() {
		c.JSON(http.StatusForbidden, gin.H{
			"message":   "Permission denied.",
			"requestId": middleware.GetRequestID(c),
		})
		return false
	}
	return true
}

func (h *CoreHandler) checkProjectReadPermission(c *gin.Context, userID uint, role iamdomain.Role) (bool, bool) {
	if h.permissionChecker == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message":   "An unexpected error occurred.",
			"requestId": middleware.GetRequestID(c),
		})
		return false, true
	}
	allowed, err := h.permissionChecker.Check(c.Request.Context(), userID, role, "core:project", "read")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message":   "An unexpected error occurred.",
			"requestId": middleware.GetRequestID(c),
		})
		return false, true
	}
	return allowed, false
}

func projectListFilterFromQuery(c *gin.Context, scope coreapp.ProjectListScope, userID uint, isAdmin bool) (coreapp.ProjectListFilter, bool) {
	filter := coreapp.ProjectListFilter{
		Scope:          scope,
		UserID:         userID,
		IsAdmin:        isAdmin,
		Status:         coredomain.ProjectStatus(strings.TrimSpace(c.Query("status"))),
		AccessType:     coredomain.ProjectAccessType(strings.TrimSpace(c.Query("accessType"))),
		ProductType:    coredomain.ProductType(strings.TrimSpace(c.Query("productType"))),
		Search:         c.Query("search"),
		TargetPlatform: c.Query("targetPlatform"),
	}
	if value := strings.TrimSpace(c.Query("looseMatch")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"message":   "Invalid query parameters.",
				"requestId": middleware.GetRequestID(c),
			})
			return filter, false
		}
		filter.LooseMatch = &parsed
	}
	if value := strings.TrimSpace(c.Query("createdFrom")); value != "" {
		parsed, ok := parseProjectTimeQuery(c, value)
		if !ok {
			return filter, false
		}
		filter.CreatedFrom = &parsed
	}
	if value := strings.TrimSpace(c.Query("createdTo")); value != "" {
		parsed, ok := parseProjectTimeQuery(c, value)
		if !ok {
			return filter, false
		}
		filter.CreatedTo = &parsed
	}
	return filter, true
}

func resourceListFilterFromQuery(c *gin.Context) (coreapp.ResourceListFilter, bool) {
	filter := coreapp.ResourceListFilter{
		ResourceType: coredomain.ResourceType(strings.TrimSpace(c.DefaultQuery("type", "all"))),
		Search:       c.Query("search"),
		Suffix:       c.Query("suffix"),
		TLD:          c.Query("tld"),
		Status:       c.Query("status"),
		Purpose:      c.Query("purpose"),
	}
	var ok bool
	filter.ForSale, ok = parseOptionalBoolQuery(c, "forSale")
	if !ok {
		return filter, false
	}
	filter.LongLived, ok = parseOptionalBoolQuery(c, "longLived")
	if !ok {
		return filter, false
	}
	filter.GraphAvailable, ok = parseOptionalBoolQuery(c, "graphAvailable")
	if !ok {
		return filter, false
	}
	filter.CreatedFrom, ok = parseOptionalCoreTimeQuery(c, "createdFrom")
	if !ok {
		return filter, false
	}
	filter.CreatedTo, ok = parseOptionalCoreTimeQuery(c, "createdTo")
	if !ok {
		return filter, false
	}
	return filter, true
}

func toResourceListFacetsResponse(facets *coreapp.ResourceListFacets) *ResourceListFacetsResponse {
	if facets == nil {
		return nil
	}
	suffixes := make([]ResourceKeyFacetResponse, len(facets.Suffixes))
	for i := range facets.Suffixes {
		suffixes[i] = ResourceKeyFacetResponse{
			Key:   facets.Suffixes[i].Key,
			Count: facets.Suffixes[i].Count,
		}
	}
	tlds := make([]ResourceKeyFacetResponse, len(facets.TLDs))
	for i := range facets.TLDs {
		tlds[i] = ResourceKeyFacetResponse{
			Key:   facets.TLDs[i].Key,
			Count: facets.TLDs[i].Count,
		}
	}
	return &ResourceListFacetsResponse{
		Status: ResourceFacetCountsResponse{
			All:         facets.Status.All,
			Normal:      facets.Status.Normal,
			Pending:     facets.Status.Pending,
			Validating:  facets.Status.Validating,
			Identifying: facets.Status.Identifying,
			Abnormal:    facets.Status.Abnormal,
			Disabled:    facets.Status.Disabled,
		},
		Private: ResourceBooleanFacetsResponse{
			All: facets.Private.All,
			Yes: facets.Private.Yes,
			No:  facets.Private.No,
		},
		LongLived: ResourceBooleanFacetsResponse{
			All: facets.LongLived.All,
			Yes: facets.LongLived.Yes,
			No:  facets.LongLived.No,
		},
		GraphAvailable: ResourceBooleanFacetsResponse{
			All: facets.GraphAvailable.All,
			Yes: facets.GraphAvailable.Yes,
			No:  facets.GraphAvailable.No,
		},
		Suffixes: suffixes,
		TLDs:     tlds,
	}
}

func parseOptionalBoolQuery(c *gin.Context, name string) (*bool, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return nil, true
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return nil, false
	}
	return &parsed, true
}

func parseOptionalCoreTimeQuery(c *gin.Context, name string) (*time.Time, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

func parseProjectTimeQuery(c *gin.Context, value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return time.Time{}, false
	}
	return parsed, true
}

type projectRequestFields interface {
	projectName() string
	projectTargetPlatform() string
	projectLogoURL() string
	projectDescription() string
	projectAccessType() string
	projectAccessUserIDs() []uint
	projectLooseMatch() *bool
	projectProducts() []ProjectProductRequest
	projectMailRules() []ProjectMailRuleRequest
}

func (req CreateProjectApplicationRequest) projectName() string { return req.Name }
func (req CreateProjectApplicationRequest) projectTargetPlatform() string {
	return req.TargetPlatform
}
func (req CreateProjectApplicationRequest) projectLogoURL() string     { return req.LogoURL }
func (req CreateProjectApplicationRequest) projectDescription() string { return req.Description }
func (req CreateProjectApplicationRequest) projectAccessType() string  { return req.AccessType }
func (req CreateProjectApplicationRequest) projectAccessUserIDs() []uint {
	return nil
}
func (req CreateProjectApplicationRequest) projectLooseMatch() *bool { return req.LooseMatch }
func (req CreateProjectApplicationRequest) projectProducts() []ProjectProductRequest {
	return nil
}
func (req CreateProjectApplicationRequest) projectMailRules() []ProjectMailRuleRequest {
	return req.MailRules
}

func (req AdminCreateProjectRequest) projectName() string { return req.Name }
func (req AdminCreateProjectRequest) projectTargetPlatform() string {
	return req.TargetPlatform
}
func (req AdminCreateProjectRequest) projectLogoURL() string     { return req.LogoURL }
func (req AdminCreateProjectRequest) projectDescription() string { return req.Description }
func (req AdminCreateProjectRequest) projectAccessType() string  { return req.AccessType }
func (req AdminCreateProjectRequest) projectAccessUserIDs() []uint {
	return req.AccessUserIDs
}
func (req AdminCreateProjectRequest) projectLooseMatch() *bool { return req.LooseMatch }
func (req AdminCreateProjectRequest) projectProducts() []ProjectProductRequest {
	return req.Products
}
func (req AdminCreateProjectRequest) projectMailRules() []ProjectMailRuleRequest {
	return req.MailRules
}

func toAppProjectRequest(req projectRequestFields) coreapp.CreateProjectRequest {
	looseMatch := true
	if req.projectLooseMatch() != nil {
		looseMatch = *req.projectLooseMatch()
	}
	requestProducts := req.projectProducts()
	products := make([]coreapp.ProjectProductRequest, len(requestProducts))
	for i := range requestProducts {
		products[i] = coreapp.ProjectProductRequest{
			Type:                    requestProducts[i].Type,
			Status:                  requestProducts[i].Status,
			CodeEnabled:             requestProducts[i].CodeEnabled,
			PurchaseEnabled:         requestProducts[i].PurchaseEnabled,
			CodePrice:               requestProducts[i].CodePrice,
			PurchasePrice:           requestProducts[i].PurchasePrice,
			CodeSupplierPrice:       requestProducts[i].CodeSupplierPrice,
			PurchaseSupplierPrice:   requestProducts[i].PurchaseSupplierPrice,
			CodeWindowMinutes:       requestProducts[i].CodeWindowMinutes,
			ActivationWindowMinutes: requestProducts[i].ActivationWindowMinutes,
			WarrantyMinutes:         requestProducts[i].WarrantyMinutes,
			MainWeight:              requestProducts[i].MainWeight,
			DotWeight:               requestProducts[i].DotWeight,
			PlusWeight:              requestProducts[i].PlusWeight,
		}
	}
	requestRules := req.projectMailRules()
	rules := make([]coreapp.ProjectMailRuleRequest, len(requestRules))
	for i := range requestRules {
		rules[i] = coreapp.ProjectMailRuleRequest{
			RuleType: requestRules[i].RuleType,
			Pattern:  requestRules[i].Pattern,
			Enabled:  requestRules[i].Enabled,
		}
	}
	return coreapp.CreateProjectRequest{
		Name:           req.projectName(),
		TargetPlatform: req.projectTargetPlatform(),
		LogoURL:        req.projectLogoURL(),
		Description:    req.projectDescription(),
		AccessType:     req.projectAccessType(),
		AccessUserIDs:  req.projectAccessUserIDs(),
		LooseMatch:     looseMatch,
		Products:       products,
		MailRules:      rules,
	}
}

func toAppProjectBulkSelection(req ProjectBulkSelectionRequest) coreapp.ProjectBulkSelection {
	return coreapp.ProjectBulkSelection{
		Mode:       coreapp.ProjectSelectionMode(req.Mode),
		ProjectIDs: req.ProjectIDs,
		Filter: coreapp.ProjectListFilter{
			Scope:          coreapp.ProjectListScopeAll,
			IsAdmin:        true,
			Status:         coredomain.ProjectStatus(strings.TrimSpace(req.Filter.Status)),
			AccessType:     coredomain.ProjectAccessType(strings.TrimSpace(req.Filter.AccessType)),
			LooseMatch:     req.Filter.LooseMatch,
			ProductType:    coredomain.ProductType(strings.TrimSpace(req.Filter.ProductType)),
			Search:         req.Filter.Search,
			TargetPlatform: req.Filter.TargetPlatform,
			CreatedFrom:    req.Filter.CreatedFrom,
			CreatedTo:      req.Filter.CreatedTo,
		},
	}
}

func toProjectItemResponse(summary coreapp.ProjectSummary, includeInternal bool, viewerUserID uint) ProjectItemResponse {
	project := summary.Project
	item := ProjectItemResponse{
		ID:             project.ID,
		Name:           project.Name,
		TargetPlatform: project.TargetPlatform,
		LogoURL:        project.LogoURL,
		Description:    project.Description,
		Status:         string(project.Status),
		AccessType:     string(project.AccessType),
		LooseMatch:     project.LooseMatch,
		ProductCount:   summary.ProductCount,
		MailRuleCount:  summary.MailRuleCount,
		Products:       toProjectProductSummaryResponses(summary.Products),
		CreatedAt:      project.CreatedAt,
		UpdatedAt:      project.UpdatedAt,
	}
	if includeInternal || isOwnProjectApplication(project, viewerUserID) {
		if summary.Owner != nil {
			item.Owner = &adminMicrosoftOwnerResponse{
				ID:        summary.Owner.ID,
				Email:     summary.Owner.Email,
				Nickname:  summary.Owner.Nickname,
				GroupName: summary.Owner.GroupName,
				Role:      summary.Owner.Role,
				Enabled:   summary.Owner.Enabled,
			}
		}
		item.ApplicantUserID = project.ApplicantUserID
		item.ReviewReason = project.ReviewReason
	}
	return item
}

func isOwnProjectApplication(project coredomain.Project, viewerUserID uint) bool {
	return viewerUserID != 0 &&
		project.ApplicantUserID != nil &&
		*project.ApplicantUserID == viewerUserID &&
		project.Status != coredomain.ProjectStatusListed
}

func toProjectListFacetsResponse(facets *coreapp.ProjectListFacets) *ProjectListFacetsResponse {
	if facets == nil {
		return nil
	}
	return &ProjectListFacetsResponse{
		Status: ProjectStatusFacetsResponse{
			All:       facets.Status.All,
			Listed:    facets.Status.Listed,
			Reviewing: facets.Status.Reviewing,
			Rejected:  facets.Status.Delisted,
		},
		Access: ProjectAccessFacetsResponse{
			All:     facets.Access.All,
			Public:  facets.Access.Public,
			Private: facets.Access.Private,
		},
		Match: ProjectMatchFacetsResponse{
			All:    facets.Match.All,
			Loose:  facets.Match.Loose,
			Strict: facets.Match.Strict,
		},
		ProductType: ProjectProductTypeFacetsResponse{
			All:       facets.ProductType.All,
			Microsoft: facets.ProductType.Microsoft,
			Domain:    facets.ProductType.Domain,
		},
	}
}

func toProjectProductSummaryResponses(products []coredomain.Product) []ProjectProductSummaryResponse {
	if len(products) == 0 {
		return nil
	}
	items := make([]ProjectProductSummaryResponse, len(products))
	for i := range products {
		product := products[i]
		items[i] = ProjectProductSummaryResponse{
			ID:                      product.ID,
			Type:                    string(product.Type),
			Status:                  string(product.Status),
			CodeEnabled:             product.CodeEnabled,
			PurchaseEnabled:         product.PurchaseEnabled,
			CodePrice:               product.CodePrice,
			PurchasePrice:           product.PurchasePrice,
			CodeWindowMinutes:       product.CodeWindowMinutes,
			ActivationWindowMinutes: product.ActivationWindowMinutes,
			WarrantyMinutes:         product.WarrantyMinutes,
		}
	}
	return items
}

func toProjectDetailResponse(detail *coredomain.ProjectDetail, includeInternal bool, viewerUserID uint) ProjectDetailResponse {
	exposeApplicationFields := includeInternal || isOwnProjectApplication(detail.Project, viewerUserID)
	item := toProjectItemResponse(coreapp.ProjectSummary{
		Project:       detail.Project,
		ProductCount:  len(detail.Products),
		MailRuleCount: len(detail.MailRules),
	}, includeInternal, viewerUserID)
	products := make([]ProjectProductResponse, len(detail.Products))
	for i := range detail.Products {
		product := detail.Products[i]
		products[i] = ProjectProductResponse{
			ID:                      product.ID,
			ProjectID:               product.ProjectID,
			Type:                    string(product.Type),
			Status:                  string(product.Status),
			CodeEnabled:             product.CodeEnabled,
			PurchaseEnabled:         product.PurchaseEnabled,
			CodePrice:               product.CodePrice,
			PurchasePrice:           product.PurchasePrice,
			CodeWindowMinutes:       product.CodeWindowMinutes,
			ActivationWindowMinutes: product.ActivationWindowMinutes,
			WarrantyMinutes:         product.WarrantyMinutes,
			CreatedAt:               product.CreatedAt,
			UpdatedAt:               product.UpdatedAt,
		}
		if includeInternal {
			products[i].CodeSupplierPrice = product.CodeSupplierPrice
			products[i].PurchaseSupplierPrice = product.PurchaseSupplierPrice
			products[i].MainWeight = intPtr(product.MainWeight)
			products[i].DotWeight = intPtr(product.DotWeight)
			products[i].PlusWeight = intPtr(product.PlusWeight)
		}
	}
	var rules []ProjectMailRuleResponse
	if exposeApplicationFields {
		rules = make([]ProjectMailRuleResponse, len(detail.MailRules))
		for i := range detail.MailRules {
			rule := detail.MailRules[i]
			rules[i] = ProjectMailRuleResponse{
				ID:        rule.ID,
				ProjectID: rule.ProjectID,
				RuleType:  string(rule.RuleType),
				Pattern:   rule.Pattern,
				Enabled:   rule.Enabled,
				CreatedAt: rule.CreatedAt,
				UpdatedAt: rule.UpdatedAt,
			}
		}
	}
	accesses := []ProjectAccessResponse(nil)
	if includeInternal {
		accesses = toProjectAccessResponses(detail.Accesses)
	}
	return ProjectDetailResponse{
		Project:   item,
		Products:  products,
		MailRules: rules,
		Accesses:  accesses,
	}
}

func intPtr(value int) *int {
	return &value
}

func toProjectAccessResponses(accesses []coredomain.ProjectAccess) []ProjectAccessResponse {
	if len(accesses) == 0 {
		return nil
	}
	items := make([]ProjectAccessResponse, len(accesses))
	for i := range accesses {
		items[i] = toProjectAccessResponse(accesses[i])
	}
	return items
}

func toProjectAccessResponse(access coredomain.ProjectAccess) ProjectAccessResponse {
	return ProjectAccessResponse{
		ID:        access.ID,
		ProjectID: access.ProjectID,
		UserID:    access.UserID,
		GrantedBy: access.GrantedBy,
		CreatedAt: access.CreatedAt,
	}
}

// GET /v1/servers
func (h *CoreHandler) GetServers(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	if !requireSupplier(c) {
		return
	}

	scope := c.DefaultQuery("scope", "owned")
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}

	if scope == "all" {
		role, ok := middleware.GetCurrentRole(c)
		if !ok || !role.HasAdminAccess() {
			scope = "owned"
		}
	}

	result, err := h.module.ServerUseCase.List(c.Request.Context(), userID, scope, offset, limit)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	items := make([]ServerItemResponse, len(result.Items))
	for i, s := range result.Items {
		items[i] = ServerItemResponse{
			ID:            s.ID,
			Name:          s.Name,
			ServerAddress: s.ServerAddress,
			Status:        string(s.Status),
			CreatedAt:     s.CreatedAt,
		}
	}

	c.JSON(http.StatusOK, ServerListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

// POST /v1/servers
func (h *CoreHandler) PostServer(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	if !requireSupplier(c) {
		return
	}

	var req CreateMailServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	appReq := &coreapp.CreateServerRequest{
		Name:          req.Name,
		ServerAddress: req.ServerAddress,
		MXRecord:      req.MXRecord,
		SPFRecord:     req.SPFRecord,
		DKIMRecord:    req.DKIMRecord,
		DMARCRecord:   req.DMARCRecord,
		PTRRecord:     req.PTRRecord,
	}

	server, err := h.module.ServerUseCase.Create(c.Request.Context(), userID, appReq)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusCreated, ServerCreateResponse{
		ID:            server.ID,
		Name:          server.Name,
		ServerAddress: server.ServerAddress,
		Status:        string(server.Status),
		CreatedAt:     server.CreatedAt,
	})
}

// --- Domain Resources ---

// POST /v1/domains
func (h *CoreHandler) PostDomain(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	var req CreateDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	purpose := strings.TrimSpace(req.Purpose)
	if purpose == "" {
		purpose = string(coredomain.PurposeNotSale)
	}
	role, ok := middleware.GetCurrentRole(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	switch coredomain.ResourcePurpose(purpose) {
	case coredomain.PurposeNotSale:
	case coredomain.PurposeSale:
		writeCoreError(c, coredomain.ErrInvalidPurpose)
		return
	case coredomain.PurposeBinding:
		if !role.HasAdminAccess() {
			c.JSON(http.StatusForbidden, gin.H{
				"message":   "Permission denied.",
				"requestId": middleware.GetRequestID(c),
			})
			return
		}
	}

	appReq := &coreapp.CreateDomainRequest{
		Domain:       req.Domain,
		MailServerID: req.MailServerID,
		Purpose:      purpose,
		AllowBinding: role.HasAdminAccess(),
	}

	result, err := h.module.DomainUseCase.Create(c.Request.Context(), userID, appReq)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusCreated, DomainResourceDetailResponse{
		ID:           result.ID,
		Domain:       result.Domain,
		MailServerID: result.MailServerID,
		Purpose:      string(result.Purpose),
		Status:       string(result.Status),
		CreatedAt:    result.CreatedAt,
	})
}

// GET /v1/domains/:domainId/mailboxes
func (h *CoreHandler) GetDomainMailboxes(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	role, _ := middleware.GetCurrentRole(c)
	isAdmin := role.HasAdminAccess()

	domainIDStr := c.Param("domainId")
	domainID, err := strconv.ParseUint(domainIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid domain ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}

	result, err := h.module.MailboxUseCase.List(c.Request.Context(), uint(domainID), userID, isAdmin, offset, limit)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	items := make([]MailboxItemResponse, len(result.Items))
	for i, mb := range result.Items {
		items[i] = MailboxItemResponse{
			ID:              mb.ID,
			Email:           mb.Email,
			Status:          string(mb.Status),
			LastAllocatedAt: mb.LastAllocatedAt,
			CreatedAt:       mb.CreatedAt,
		}
	}

	c.JSON(http.StatusOK, MailboxListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func toAppBulkSelection(selection ResourceBulkSelectionRequest) coreapp.ResourceBulkSelection {
	return coreapp.ResourceBulkSelection{
		Mode:        coreapp.ResourceBulkSelectionMode(selection.Mode),
		ResourceIDs: selection.ResourceIDs,
		Filter: coreapp.ResourceBulkFilter{
			ResourceType:   coredomain.ResourceType(selection.Filter.ResourceType),
			Search:         selection.Filter.Search,
			Suffix:         selection.Filter.Suffix,
			TLD:            selection.Filter.TLD,
			Status:         selection.Filter.Status,
			Purpose:        selection.Filter.Purpose,
			ForSale:        selection.Filter.ForSale,
			LongLived:      selection.Filter.LongLived,
			GraphAvailable: selection.Filter.GraphAvailable,
			CreatedFrom:    selection.Filter.CreatedFrom,
			CreatedTo:      selection.Filter.CreatedTo,
		},
	}
}

func validateBulkSelectionRequest(selection ResourceBulkSelectionRequest) map[string]string {
	fields := make(map[string]string)
	switch selection.Mode {
	case "ids":
		if len(selection.ResourceIDs) == 0 {
			fields["selection.resourceIds"] = "At least one resource ID is required."
		} else {
			for _, resourceID := range selection.ResourceIDs {
				if resourceID == 0 {
					fields["selection.resourceIds"] = "Resource IDs must be positive."
					break
				}
			}
		}
	case "filter":
		if selection.Filter.ResourceType == "" {
			fields["selection.filter.resourceType"] = "Resource type is required."
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func validateProjectBulkSelectionRequest(selection ProjectBulkSelectionRequest) map[string]string {
	fields := make(map[string]string)
	switch selection.Mode {
	case "ids":
		if len(selection.ProjectIDs) == 0 {
			fields["selection.projectIds"] = "At least one project ID is required."
		} else {
			for _, projectID := range selection.ProjectIDs {
				if projectID == 0 {
					fields["selection.projectIds"] = "Project IDs must be positive."
					break
				}
			}
		}
	case "filter":
	default:
		fields["selection.mode"] = "Selection mode must be ids or filter."
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// --- Helpers ---

func parseResourceID(c *gin.Context) (uint, bool) {
	return parseUintParam(c, "resourceId", "Invalid resource ID.")
}

func parseUintParam(c *gin.Context, name string, message string) (uint, bool) {
	idStr := c.Param(name)
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   message,
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return uint(id), true
}

func parsePagination(c *gin.Context) (int, int, bool) {
	return middleware.ParsePagination(c, middleware.PaginationOptions{
		DefaultLimit: 20,
		MaxLimit:     10000,
	})
}

func parseOptionalUintQuery(c *gin.Context, name string) (uint, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid " + name + ".",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return uint(value), true
}

func writeCoreError(c *gin.Context, err error) {
	rid := middleware.GetRequestID(c)

	switch {
	case errors.Is(err, coredomain.ErrResourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Resource not found.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidResourceType):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid resource type.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidResourceStatus):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid resource status.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidResourceFilter):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid resource filter.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrResourceSelectionTooLarge):
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Too many resource IDs; use filter mode for larger selections.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrResourceNotPrivate):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Only private resources can be deleted or published.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrForbiddenPurpose):
		c.JSON(http.StatusForbidden, gin.H{
			"message":   "Permission denied.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrDuplicateEmail):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "An email address in the import already exists.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrDuplicateDomain):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Domain already exists.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrResourceHasAllocation):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Resource has an active allocation.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidImportFormat):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid import format.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrFileStorageUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"message":   "File storage is temporarily unavailable.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrImportQueueUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"message":   "Resource import queue is temporarily unavailable.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrMailServerNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Mail server not found.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrMailServerOwnerMismatch):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Mail server not found.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidPurpose):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid purpose for this command.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidDomain):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid domain.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrForbiddenResource):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Resource not found.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrProjectNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Project not found.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrForbiddenProject):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Project not found.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrDuplicateProject):
		c.JSON(http.StatusConflict, gin.H{
			"message":   "Project already exists.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidProject):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid project.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidProjectStatus):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid project status.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidProduct):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid project product.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidMailRule):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid project mail rule.",
			"requestId": rid,
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{
			"message":   "An unexpected error occurred.",
			"requestId": rid,
		})
	}
}

func validationErrors(err error) map[string]string {
	type validator interface {
		Field() string
		Tag() string
	}

	fields := make(map[string]string)
	if errs, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range errs.Unwrap() {
			if v, ok := e.(validator); ok {
				fields[v.Field()] = v.Tag() + " validation failed"
			}
		}
	} else {
		fields["body"] = err.Error()
	}
	return fields
}
