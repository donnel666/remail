package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/donnel666/remail/api/middleware"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

// MaxImportBytes limits TXT import content to prevent memory exhaustion.
const MaxImportBytes = 100 * 1024 * 1024 // 100 MB

// CoreHandler holds the Core HTTP handlers.
type CoreHandler struct {
	module *CoreModule
}

// NewCoreHandler creates a new Core handler.
func NewCoreHandler(module *CoreModule) *CoreHandler {
	return &CoreHandler{module: module}
}

// --- Resources ---

// GET /v1/resources
func (h *CoreHandler) GetResources(c *gin.Context) {
	userID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	scope := c.DefaultQuery("scope", "owned")
	resourceType := c.DefaultQuery("type", "all")
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}

	// Non-admin users can only see their own resources
	if scope == "all" {
		roleLevel, ok := middleware.GetCurrentRoleLevel(c)
		if !ok || !roleLevel.IsAtLeast(iamdomain.RoleAdmin) {
			scope = "owned"
		}
	}

	result, err := h.module.ResourceUseCase.List(c.Request.Context(), userID, scope, resourceType, offset, limit)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	items := make([]ResourceItemResponse, len(result.Items))
	for i, item := range result.Items {
		items[i] = ResourceItemResponse{
			ID:            item.ID,
			Type:          string(item.Type),
			OwnerID:       item.OwnerID,
			Status:        item.Status,
			ForSale:       item.ForSale,
			LongLived:     item.LongLived,
			LastSafeError: item.LastSafeError,
			Email:         item.Email,
			Domain:        item.Domain,
			Purpose:       item.Purpose,
			CreatedAt:     item.CreatedAt,
		}
	}

	c.JSON(http.StatusOK, ResourceListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
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

	if err := h.module.ResourceUseCase.DeletePrivateMicrosoft(
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

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid import file.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	result, err := h.module.ImportUseCase.AcceptMicrosoftTXTFile(c.Request.Context(), userID, header.Filename, content, longLived, middleware.GetRequestID(c))
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusAccepted, ImportResponse{ImportID: result.ImportID, Imported: result.Imported})
}

// GET /v1/resource-imports/:importId
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

	detail, err := h.module.ResourceUseCase.PublishMicrosoftForSale(
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

	c.JSON(http.StatusOK, MicrosoftResourceDetailResponse{
		ID:              detail.ID,
		EmailAddress:    detail.EmailAddress,
		ForSale:         detail.ForSale,
		LongLived:       detail.LongLived,
		Status:          detail.Status,
		QualityScore:    detail.QualityScore,
		LastSafeError:   detail.LastSafeError,
		LastAllocatedAt: detail.LastAllocatedAt,
		CreatedAt:       detail.CreatedAt,
	})
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

	result, err := h.module.ResourceUseCase.PublishMicrosoftForSaleBatch(
		c.Request.Context(),
		req.ResourceIDs,
		userID,
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusOK, PublishResourcesResponse{
		Requested: result.Requested,
		Published: result.Published,
	})
}

// POST /v1/resources/:resourceId/validate
func (h *CoreHandler) PostResourceValidate(c *gin.Context) {
	// P1-I2: stub — actual Microsoft ACL / SMTP validation will be implemented in P1-I3.
	if _, ok := parseResourceID(c); !ok {
		return
	}
	if _, ok := requireCurrentUserID(c); !ok {
		return
	}

	c.JSON(http.StatusNotImplemented, gin.H{
		"message":   "Resource validation is not yet implemented. It will be available in the next iteration.",
		"requestId": middleware.GetRequestID(c),
	})
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
	roleLevel, ok := middleware.GetCurrentRoleLevel(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return false
	}
	if !roleLevel.IsAtLeast(iamdomain.RoleSupplier) {
		c.JSON(http.StatusForbidden, gin.H{
			"message":   "Permission denied.",
			"requestId": middleware.GetRequestID(c),
		})
		return false
	}
	return true
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
		roleLevel, ok := middleware.GetCurrentRoleLevel(c)
		if !ok || !roleLevel.IsAtLeast(iamdomain.RoleAdmin) {
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

	if !requireSupplier(c) {
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

	appReq := &coreapp.CreateDomainRequest{
		Domain:       req.Domain,
		MailServerID: req.MailServerID,
		Purpose:      req.Purpose,
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
	if !requireSupplier(c) {
		return
	}

	userID, _ := middleware.GetCurrentUserID(c)
	roleLevel, _ := middleware.GetCurrentRoleLevel(c)
	isAdmin := roleLevel.IsAtLeast(iamdomain.RoleAdmin)

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

// --- Helpers ---

func parseResourceID(c *gin.Context) (uint, bool) {
	idStr := c.Param("resourceId")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid resource ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return uint(id), true
}

func parsePagination(c *gin.Context) (int, int, bool) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, 0, false
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, 0, false
	}
	return offset, limit, true
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
	case errors.Is(err, coredomain.ErrResourceNotPrivate):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Only private Microsoft resources can be deleted.",
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
		c.JSON(http.StatusForbidden, gin.H{
			"message":   "Mail server owner mismatch.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrInvalidPurpose):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message":   "Invalid purpose. Must be 'sale' or 'auxiliary'.",
			"requestId": rid,
		})
	case errors.Is(err, coredomain.ErrForbiddenResource):
		c.JSON(http.StatusNotFound, gin.H{
			"message":   "Resource not found.",
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
