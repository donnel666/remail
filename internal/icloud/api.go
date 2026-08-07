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

// RegisterRoutes exposes only the safe import lifecycle. Resource session
// data, import artifacts, and HME request context remain write-only.
func RegisterRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	if rg == nil || module == nil || module.Service == nil {
		return
	}
	h := &handler{service: module.Service}
	resources := rg.Group("/admin/icloud/resources")
	resources.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	resources.POST("/imports", middleware.PermissionRequired(checker, "core:resource", "write"), h.importResources)
	resources.GET("/imports/:importId", middleware.PermissionRequired(checker, "core:resource", "read"), h.resourceImport)
	resources.POST("/:resourceId/validation", middleware.PermissionRequired(checker, "core:resource", "operate"), h.validateResource)
}

type handler struct{ service *Service }

type iCloudImportTaskResponse struct {
	TaskID            string     `json:"taskId"`
	BizType           string     `json:"bizType"`
	BizID             uint       `json:"bizId"`
	Kind              string     `json:"kind"`
	Status            string     `json:"status"`
	Attempts          int        `json:"attempts"`
	MaxAttempts       int        `json:"maxAttempts"`
	RemainingAttempts int        `json:"remainingAttempts"`
	QueuedAt          time.Time  `json:"queuedAt"`
	StartedAt         *time.Time `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
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
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
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

func (h *handler) validateResource(c *gin.Context) {
	resourceID, err := strconv.ParseUint(strings.TrimSpace(c.Param("resourceId")), 10, 64)
	if err != nil || resourceID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid resource ID.", "requestId": middleware.GetRequestID(c)})
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	if err := h.service.RequestAdminICloudValidation(
		c.Request.Context(), operatorUserID, uint(resourceID), middleware.GetRequestID(c), c.FullPath(),
	); err != nil {
		writeICloudError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"resourceId": uint(resourceID), "status": iCloudResourcePending})
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
			Status: taskStatus, Attempts: item.Attempts, MaxAttempts: item.MaxAttempts, RemainingAttempts: remaining,
			QueuedAt: item.CreatedAt, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, UpdatedAt: item.UpdatedAt,
		},
	}
}

func writeICloudError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, ErrICloudImportInvalid), errors.Is(err, ErrICloudImportInvalidOwner):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid iCloud resource command.", "requestId": requestID})
	case errors.Is(err, ErrICloudImportConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency key was already used for a different command.", "requestId": requestID})
	case errors.Is(err, ErrICloudImportNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource import not found.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "iCloud resource not found.", "requestId": requestID})
	case errors.Is(err, ErrICloudResourceStatus):
		c.JSON(http.StatusConflict, gin.H{"message": "iCloud resource status does not allow validation.", "requestId": requestID})
	case errors.Is(err, ErrICloudImportDependency), errors.Is(err, ErrICloudImportStorage), errors.Is(err, ErrICloudImportTemporary), errors.Is(err, ErrICloudValidationTemp):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "iCloud resource service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
