package icloud

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	coreDomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes exposes the safe administrator lifecycle. Resource session
// data, import artifacts, and HME request context remain write-only.
func RegisterRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	if rg == nil || module == nil || module.Service == nil {
		return
	}
	h := &handler{service: module.Service, checker: checker}
	resources := rg.Group("/admin/icloud/resources")
	resources.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	resources.GET("", middleware.PermissionRequired(checker, "core:resource", "read"), h.listResources)
	resources.POST("/imports", middleware.PermissionRequired(checker, "core:resource", "write"), h.importResources)
	resources.GET("/imports/:importId", middleware.PermissionRequired(checker, "core:resource", "read"), h.resourceImport)
	resources.POST("/batch/validation", middleware.PermissionRequired(checker, "core:resource", "operate"), h.batchResourceCommand(AdminICloudValidate))
	resources.POST("/batch/alias", middleware.PermissionRequired(checker, "core:resource", "operate"), h.batchResourceCommand(AdminICloudAlias))
	resources.POST("/batch/disable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.batchResourceCommand(AdminICloudDisable))
	resources.POST("/batch/publish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.batchResourceCommand(AdminICloudPublish))
	resources.POST("/batch/unpublish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.batchResourceCommand(AdminICloudUnpublish))
	resources.POST("/batch/delete", middleware.PermissionRequired(checker, "core:resource", "operate"), h.batchResourceCommand(AdminICloudDelete))
	resources.GET("/:resourceId", middleware.PermissionRequired(checker, "core:resource", "read"), h.getResource)
	resources.GET("/:resourceId/aliases", middleware.PermissionRequired(checker, "core:resource", "read"), h.listAliases)
	resources.POST("/:resourceId/aliases", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudAlias))
	resources.PATCH("/:resourceId", middleware.PermissionRequired(checker, "core:resource", "write"), h.patchResource)
	resources.POST("/:resourceId/validation", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudValidate))
	resources.POST("/:resourceId/enable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudEnable))
	resources.POST("/:resourceId/disable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudDisable))
	resources.POST("/:resourceId/publish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudPublish))
	resources.POST("/:resourceId/unpublish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudUnpublish))
	resources.POST("/:resourceId/recover", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudRecover))
	resources.DELETE("/:resourceId", middleware.PermissionRequired(checker, "core:resource", "operate"), h.resourceCommand(AdminICloudDelete))
}

type handler struct {
	service *Service
	checker middleware.PermissionChecker
}

func (h *handler) listResources(c *gin.Context) {
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(adminICloudResourceDefaultLimit)))
	if offsetErr != nil || limitErr != nil {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	var forSale *bool
	if value, exists := c.GetQuery("forSale"); exists {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			writeICloudError(c, ErrICloudResourceQuery)
			return
		}
		forSale = &parsed
	}
	createdFrom, ok := parseICloudQueryTime(c.Query("createdFrom"))
	if !ok {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	createdTo, ok := parseICloudQueryTime(c.Query("createdTo"))
	if !ok {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	includeFacets, ok := parseICloudQueryBool(c, "includeFacets", true)
	if !ok {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	includeTotal, ok := parseICloudQueryBool(c, "includeTotal", true)
	if !ok {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	result, err := h.service.ListAdminICloudResources(c.Request.Context(), AdminICloudResourceListFilter{
		Search: c.Query("search"), Suffix: c.Query("suffix"), Status: c.Query("status"),
		ForSale: forSale, SessionStatus: c.Query("sessionStatus"), CreatedFrom: createdFrom,
		CreatedTo: createdTo, Offset: offset, Limit: limit,
		IncludeFacets: &includeFacets, IncludeTotal: &includeTotal,
	})
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func parseICloudQueryBool(c *gin.Context, name string, defaultValue bool) (bool, bool) {
	value, exists := c.GetQuery(name)
	if !exists || strings.TrimSpace(value) == "" {
		return defaultValue, true
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return parsed, err == nil
}

func parseICloudQueryTime(value string) (*time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, false
	}
	parsed = parsed.UTC()
	return &parsed, true
}

type iCloudImportTaskResponse struct {
	TaskID             string     `json:"taskId"`
	BizType            string     `json:"bizType"`
	BizID              uint       `json:"bizId"`
	Kind               string     `json:"kind"`
	Status             string     `json:"status"`
	CredentialRevision *uint64    `json:"credentialRevision"`
	Progress           any        `json:"progress"`
	Attempts           int        `json:"attempts"`
	MaxAttempts        int        `json:"maxAttempts"`
	RemainingAttempts  int        `json:"remainingAttempts"`
	QueuedAt           time.Time  `json:"queuedAt"`
	StartedAt          *time.Time `json:"startedAt"`
	FinishedAt         *time.Time `json:"finishedAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type iCloudImportResponse struct {
	ImportID      uint                     `json:"importId"`
	TaskID        string                   `json:"taskId"`
	RequestID     string                   `json:"requestId"`
	Status        string                   `json:"status"`
	Accepted      int                      `json:"accepted"`
	Imported      int                      `json:"imported"`
	Skipped       int                      `json:"skipped"`
	LastSafeError *string                  `json:"lastSafeError"`
	Task          iCloudImportTaskResponse `json:"task"`
	Reused        bool                     `json:"reused"`
	CreatedAt     time.Time                `json:"createdAt"`
	UpdatedAt     time.Time                `json:"updatedAt"`
}

func (h *handler) importResources(c *gin.Context) {
	if !requireICloudIdempotencyKey(c) {
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	maxBytes := maxICloudImportBytesValue()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, iCloudImportMultipartMaxBytes(maxBytes))
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid import file.", "requestId": middleware.GetRequestID(c)})
		return
	}
	defer file.Close()
	ownerID, err := strconv.ParseUint(strings.TrimSpace(c.PostForm("ownerId")), 10, 64)
	if err != nil || ownerID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid ownerId value.", "requestId": middleware.GetRequestID(c)})
		return
	}
	strategy, ok := coreDomain.NormalizeImportErrorStrategy(c.PostForm("errorStrategy"))
	if !ok {
		writeICloudError(c, ErrICloudImportInvalid)
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(content)) > maxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid import file.", "requestId": middleware.GetRequestID(c)})
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	result, reused, err := h.service.AcceptAdminICloudTXTFile(
		c.Request.Context(), operatorUserID, uint(ownerID), header.Filename, content, strategy,
		idempotencyKey, middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, toICloudImportResponse(result, reused))
}

func (h *handler) resourceImport(c *gin.Context) {
	importID, err := strconv.ParseUint(strings.TrimSpace(c.Param("importId")), 10, 64)
	if err != nil || importID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid import ID.", "requestId": middleware.GetRequestID(c)})
		return
	}
	result, err := h.service.GetAdminICloudResourceImport(c.Request.Context(), uint(importID))
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, toICloudImportResponse(result, false))
}

func (h *handler) listAliases(c *gin.Context) {
	resourceID, ok := parseICloudResourceID(c)
	if !ok {
		return
	}
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(adminICloudResourceDefaultLimit)))
	if offsetErr != nil || limitErr != nil {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	result, err := h.service.ListAdminICloudAliases(c.Request.Context(), resourceID, offset, limit)
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h *handler) getResource(c *gin.Context) {
	resourceID, ok := parseICloudResourceID(c)
	if !ok {
		return
	}
	result, err := h.service.GetAdminICloudResource(c.Request.Context(), resourceID)
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

type iCloudEditRequest struct {
	Version      uint64                       `json:"version"`
	PrimaryEmail *string                      `json:"primaryEmail"`
	OwnerID      *uint                        `json:"ownerId"`
	ForSale      *bool                        `json:"forSale"`
	Credentials  *AdminICloudCredentialsInput `json:"credentials"`
}

func (h *handler) patchResource(c *gin.Context) {
	if !requireICloudIdempotencyKey(c) {
		return
	}
	resourceID, ok := parseICloudResourceID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var request iCloudEditRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Version == 0 ||
		(request.PrimaryEmail == nil && request.OwnerID == nil && request.ForSale == nil && request.Credentials == nil) {
		writeICloudError(c, ErrICloudResourceUpdate)
		return
	}
	if (request.ForSale != nil || request.Credentials != nil) && !h.requirePermission(c, "core:resource", "operate") {
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	result, err := h.service.EditAdminICloudResource(c.Request.Context(), AdminICloudEditCommand{
		ResourceID: resourceID, Version: request.Version, PrimaryEmail: request.PrimaryEmail,
		OwnerUserID: request.OwnerID, ForSale: request.ForSale, Credentials: request.Credentials,
		OperatorUserID: operatorUserID, IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID: middleware.GetRequestID(c), Path: c.FullPath(),
	})
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h *handler) requirePermission(c *gin.Context, resource, action string) bool {
	userID, userOK := middleware.GetCurrentUserID(c)
	role, roleOK := middleware.GetCurrentRole(c)
	if !userOK || !roleOK {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	if h.checker == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	allowed, err := h.checker.Check(c.Request.Context(), userID, role, resource, action)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	if !allowed {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	return true
}

func (h *handler) resourceCommand(command AdminICloudCommand) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireICloudIdempotencyKey(c) {
			return
		}
		resourceID, ok := parseICloudResourceID(c)
		if !ok {
			return
		}
		version, err := strconv.ParseUint(strings.TrimSpace(c.Query("version")), 10, 64)
		if err != nil || version == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid resource version.", "requestId": middleware.GetRequestID(c)})
			return
		}
		operatorUserID, ok := middleware.GetCurrentUserID(c)
		if !ok {
			c.Status(http.StatusUnauthorized)
			return
		}
		result, err := h.service.ApplyAdminICloudCommand(
			c.Request.Context(), command, resourceID, version, operatorUserID,
			c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
		)
		if err != nil {
			writeICloudError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		status := http.StatusOK
		if command == AdminICloudValidate || (command == AdminICloudAlias && result.Changed) {
			status = http.StatusAccepted
		}
		c.JSON(status, result)
	}
}

type iCloudBatchCommandRequest struct {
	Selection AdminICloudResourceSelection `json:"selection" binding:"required"`
}

func (h *handler) batchResourceCommand(command AdminICloudCommand) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireICloudIdempotencyKey(c) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		var request iCloudBatchCommandRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			writeICloudError(c, ErrICloudResourceSelection)
			return
		}
		operatorUserID, ok := middleware.GetCurrentUserID(c)
		if !ok {
			c.Status(http.StatusUnauthorized)
			return
		}
		result, err := h.service.ApplyAdminICloudBatch(
			c.Request.Context(), command, request.Selection, operatorUserID,
			c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
		)
		if err != nil {
			writeICloudError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, result)
	}
}

func requireICloudIdempotencyKey(c *gin.Context) bool {
	value := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if value != "" && len(value) <= 128 {
		return true
	}
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
	return false
}

func parseICloudResourceID(c *gin.Context) (uint, bool) {
	resourceID, err := strconv.ParseUint(strings.TrimSpace(c.Param("resourceId")), 10, 64)
	if err != nil || resourceID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid resource ID.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return uint(resourceID), true
}

func toICloudImportResponse(item *ImportStatusView, reused bool) iCloudImportResponse {
	taskStatus := item.TaskStatus
	if taskStatus == "" || taskStatus == "pending" {
		taskStatus = "queued"
	}
	remaining := item.MaxAttempts - item.Attempts
	if remaining < 0 {
		remaining = 0
	}
	var safeError *string
	if value := strings.TrimSpace(item.LastSafeError); value != "" {
		safeError = &value
	}
	taskID := fmt.Sprintf("icloud_import:%d", item.ImportID)
	return iCloudImportResponse{
		ImportID: item.ImportID, TaskID: taskID, RequestID: item.RequestID, Status: item.Status,
		Accepted: item.Accepted, Imported: item.Imported, Skipped: item.Skipped,
		LastSafeError: safeError, Reused: reused, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		Task: iCloudImportTaskResponse{
			TaskID: taskID, BizType: "icloud_resource_import", BizID: item.ImportID, Kind: "import",
			Status: taskStatus, CredentialRevision: nil, Attempts: item.Attempts, MaxAttempts: item.MaxAttempts, RemainingAttempts: remaining,
			QueuedAt: item.CreatedAt, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, UpdatedAt: item.UpdatedAt,
		},
	}
}

func writeICloudError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, ErrICloudResourceQuery):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid iCloud resource query.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceUpdate):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid iCloud resource update.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceSelection):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid or too large iCloud resource selection.", "requestId": requestID})
	case errors.Is(err, ErrICloudImportInvalid), errors.Is(err, ErrICloudImportInvalidOwner):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid iCloud resource command.", "requestId": requestID})
	case errors.Is(err, ErrICloudImportConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency key was already used for a different command.", "requestId": requestID})
	case errors.Is(err, ErrICloudImportNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource import not found.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "iCloud resource not found.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceStatus):
		c.JSON(http.StatusConflict, gin.H{"message": "iCloud resource status does not allow this operation.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceVersion):
		c.JSON(http.StatusConflict, gin.H{"message": "iCloud resource was changed by another operation.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceIdentity):
		c.JSON(http.StatusConflict, gin.H{"message": "iCloud email or DSID already belongs to another resource.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceAllocation):
		c.JSON(http.StatusConflict, gin.H{"message": "iCloud resource still has an active allocation.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceOwner):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "iCloud resource owner is not eligible for public supply.", "requestId": requestID})
	case errors.Is(err, ErrICloudImportDependency), errors.Is(err, ErrICloudImportStorage), errors.Is(err, ErrICloudImportTemporary), errors.Is(err, ErrICloudValidationTemp), errors.Is(err, ErrICloudResourceQueryTemporary):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "iCloud resource service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
