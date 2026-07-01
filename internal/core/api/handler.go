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
const MaxImportBytes = 5 * 1024 * 1024 // 5 MB

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
			ID:        item.ID,
			Type:      string(item.Type),
			OwnerID:   item.OwnerID,
			Status:    item.Status,
			ForSale:   item.ForSale,
			Email:     item.Email,
			Domain:    item.Domain,
			Purpose:   item.Purpose,
			CreatedAt: item.CreatedAt,
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

	userID, _ := middleware.GetCurrentUserID(c)

	if !requireSupplier(c) {
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

// POST /v1/resources/imports
func (h *CoreHandler) PostResourceImport(c *gin.Context) {
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

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid import file.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	result, err := h.module.ImportUseCase.ImportMicrosoftTXTFile(c.Request.Context(), userID, header.Filename, content, middleware.GetRequestID(c))
	if err != nil {
		writeCoreError(c, err)
		return
	}

	c.JSON(http.StatusCreated, ImportResponse{ImportID: result.ImportID, Imported: result.Imported})
}

// POST /v1/resources/:resourceId/validate
func (h *CoreHandler) PostResourceValidate(c *gin.Context) {
	// P1-I2: stub — actual Microsoft ACL / SMTP validation will be implemented in P1-I3.
	if _, ok := parseResourceID(c); !ok {
		return
	}
	if !requireSupplier(c) {
		return
	}

	c.JSON(http.StatusNotImplemented, gin.H{
		"message":   "Resource validation is not yet implemented. It will be available in the next iteration.",
		"requestId": middleware.GetRequestID(c),
	})
}

// --- Mail Servers ---

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
