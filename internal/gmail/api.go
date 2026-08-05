package gmail

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Module struct {
	Service *Service
}

func NewModule(db *gorm.DB, redisClient redis.UniversalClient, queue *asynq.Client, files governanceapp.FilePort) *Module {
	service := NewService(db, queue)
	service.SetResourceImportDependencies(redisClient, files)
	return &Module{Service: service}
}

func RegisterRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := &handler{service: module.Service}
	resources := rg.Group("/admin/gmail/resources")
	resources.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	resources.GET("", middleware.PermissionRequired(checker, "core:resource", "read"), h.localResources)
	resources.POST("/imports", middleware.PermissionRequired(checker, "core:resource", "write"), h.importLocalResources)
	resources.GET("/imports/:importId", middleware.PermissionRequired(checker, "core:resource", "read"), h.localResourceImport)
	resources.POST("/validations", middleware.PermissionRequired(checker, "core:resource", "operate"), h.validateLocalResources)
	resources.GET("/validations/:batchId", middleware.PermissionRequired(checker, "core:resource", "read"), h.localResourceValidationBatch)
	resources.POST("/:resourceId/enable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.enableLocalResource)
	resources.POST("/:resourceId/disable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.disableLocalResource)
	resources.POST("/:resourceId/validate", middleware.PermissionRequired(checker, "core:resource", "operate"), h.validateLocalResource)
	resources.POST("/:resourceId/publish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.publishLocalResource)
	resources.POST("/:resourceId/unpublish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.unpublishLocalResource)
}

type handler struct{ service *Service }

func (h *handler) localResources(c *gin.Context) {
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if offsetErr != nil || limitErr != nil {
		writeGmailError(c, ErrInvalidLocalResource)
		return
	}
	result, err := h.service.ListLocalResources(c.Request.Context(), LocalResourceListFilter{
		Search: c.Query("search"), Status: c.Query("status"), Offset: offset, Limit: limit,
	})
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

type gmailResourceImportTaskResponse struct {
	TaskID             string      `json:"taskId"`
	BizType            string      `json:"bizType"`
	BizID              uint64      `json:"bizId"`
	Kind               string      `json:"kind"`
	Status             string      `json:"status"`
	Attempts           int         `json:"attempts"`
	MaxAttempts        int         `json:"maxAttempts"`
	RemainingAttempts  int         `json:"remainingAttempts"`
	CredentialRevision *uint64     `json:"credentialRevision"`
	QueuedAt           time.Time   `json:"queuedAt"`
	StartedAt          *time.Time  `json:"startedAt"`
	FinishedAt         *time.Time  `json:"finishedAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
	Progress           interface{} `json:"progress"`
}

type gmailResourceImportResponse struct {
	ImportID      uint64                          `json:"importId"`
	TaskID        string                          `json:"taskId"`
	RequestID     string                          `json:"requestId"`
	Status        string                          `json:"status"`
	Accepted      int64                           `json:"accepted"`
	Imported      int64                           `json:"imported"`
	Skipped       int64                           `json:"skipped"`
	LastSafeError *string                         `json:"lastSafeError"`
	Task          gmailResourceImportTaskResponse `json:"task"`
	Reused        bool                            `json:"reused"`
	CreatedAt     time.Time                       `json:"createdAt"`
	UpdatedAt     time.Time                       `json:"updatedAt"`
}

func (h *handler) importLocalResources(c *gin.Context) {
	if strings.TrimSpace(c.GetHeader("Idempotency-Key")) == "" || len(strings.TrimSpace(c.GetHeader("Idempotency-Key"))) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	maxBytes := maxGmailResourceImportBytesValue()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, gmailResourceImportMultipartMaxBytes(maxBytes))
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
	if err := h.service.validateGmailResourceImportOwner(c.Request.Context(), uint(ownerID)); err != nil {
		writeGmailError(c, err)
		return
	}
	strategy, ok := normalizeGmailImportErrorStrategy(c.PostForm("errorStrategy"))
	if !ok {
		writeGmailError(c, ErrGmailImportInvalidCommand)
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
	result, reused, err := h.service.AcceptAdminGmailTXTFile(
		c.Request.Context(), operatorUserID, uint(ownerID), header.Filename, content, strategy,
		c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toGmailResourceImportResponse(result, reused))
}

func (h *handler) localResourceImport(c *gin.Context) {
	importID, err := strconv.ParseUint(strings.TrimSpace(c.Param("importId")), 10, 64)
	if err != nil || importID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid import ID.", "requestId": middleware.GetRequestID(c)})
		return
	}
	result, err := h.service.GetAdminGmailResourceImport(c.Request.Context(), importID)
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusOK, toGmailResourceImportResponse(result, false))
}

func toGmailResourceImportResponse(item *GmailResourceImportStatusView, reused bool) gmailResourceImportResponse {
	taskStatus := item.TaskStatus
	if taskStatus == "" || taskStatus == "pending" || taskStatus == "uploading" {
		taskStatus = "queued"
	}
	var safeError *string
	if value := strings.TrimSpace(item.LastSafeError); value != "" {
		safeError = &value
	}
	remaining := item.MaxAttempts - item.Attempts
	if remaining < 0 {
		remaining = 0
	}
	taskID := fmt.Sprintf("gmail_import:%d", item.ImportID)
	return gmailResourceImportResponse{
		ImportID: item.ImportID, TaskID: taskID, RequestID: item.RequestID,
		Status: item.Status, Accepted: int64(item.Accepted), Imported: int64(item.Imported), Skipped: int64(item.Skipped),
		LastSafeError: safeError, Reused: reused, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		Task: gmailResourceImportTaskResponse{
			TaskID: taskID, BizType: "gmail_resource_import", BizID: item.ImportID,
			Kind: "import", Status: taskStatus, Attempts: item.Attempts, MaxAttempts: item.MaxAttempts,
			RemainingAttempts: remaining, QueuedAt: item.CreatedAt, StartedAt: item.StartedAt,
			FinishedAt: item.FinishedAt, UpdatedAt: item.UpdatedAt,
		},
	}
}

func (h *handler) enableLocalResource(c *gin.Context) {
	h.setLocalResourceEnabled(c, true)
}

func (h *handler) disableLocalResource(c *gin.Context) {
	h.setLocalResourceEnabled(c, false)
}

func (h *handler) setLocalResourceEnabled(c *gin.Context, enabled bool) {
	resourceID, version, ok := parseLocalResourceVersionCommand(c)
	if !ok {
		return
	}
	if !validGmailIdempotencyKey(c.GetHeader("Idempotency-Key")) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	if err := h.service.SetAdminLocalResourceEnabled(
		c.Request.Context(), resourceID, version, enabled, operatorUserID, c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	); err != nil {
		writeGmailError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) validateLocalResource(c *gin.Context) {
	resourceID, err := strconv.ParseUint(strings.TrimSpace(c.Param("resourceId")), 10, 64)
	if err != nil || resourceID == 0 {
		writeGmailError(c, ErrLocalResourceMissing)
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if !validGmailIdempotencyKey(idempotencyKey) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	reused, err := h.service.RequestAdminLocalResourceValidation(
		c.Request.Context(), uint(resourceID), operatorUserID, idempotencyKey, middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"requested": 1, "queued": 1, "reused": reused})
}

type localResourceValidationBatchRequest struct {
	ResourceIDs []uint `json:"resourceIds" binding:"required"`
}

func (h *handler) validateLocalResources(c *gin.Context) {
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if !validGmailIdempotencyKey(idempotencyKey) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var request localResourceValidationBatchRequest
	if c.ShouldBindJSON(&request) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	result, err := h.service.AcceptAdminLocalResourceValidationBatch(
		c.Request.Context(), request.ResourceIDs, operatorUserID, idempotencyKey,
		middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, result)
}

func (h *handler) localResourceValidationBatch(c *gin.Context) {
	result, err := h.service.GetAdminLocalResourceValidationBatch(c.Request.Context(), c.Param("batchId"))
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h *handler) publishLocalResource(c *gin.Context) {
	h.setLocalResourceForSale(c, true)
}

func (h *handler) unpublishLocalResource(c *gin.Context) {
	h.setLocalResourceForSale(c, false)
}

func (h *handler) setLocalResourceForSale(c *gin.Context, forSale bool) {
	resourceID, version, ok := parseLocalResourceVersionCommand(c)
	if !ok {
		return
	}
	if !validGmailIdempotencyKey(c.GetHeader("Idempotency-Key")) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	if err := h.service.SetAdminLocalResourceForSale(
		c.Request.Context(), resourceID, version, forSale, operatorUserID, c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	); err != nil {
		writeGmailError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func parseLocalResourceVersionCommand(c *gin.Context) (uint, uint64, bool) {
	resourceID, resourceErr := strconv.ParseUint(strings.TrimSpace(c.Param("resourceId")), 10, 64)
	version, versionErr := strconv.ParseUint(strings.TrimSpace(c.Query("version")), 10, 64)
	if resourceErr != nil || resourceID == 0 || versionErr != nil || version == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid resource version.", "requestId": middleware.GetRequestID(c)})
		return 0, 0, false
	}
	return uint(resourceID), version, true
}

func validGmailIdempotencyKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128
}

func writeGmailError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, ErrInvalidRoute):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid Gmail request.", "requestId": requestID})
	case errors.Is(err, ErrInvalidLocalResource):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid Gmail resource input.", "requestId": requestID})
	case errors.Is(err, ErrGmailImportInvalidCommand), errors.Is(err, ErrGmailImportInvalidOwner):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid resource command.", "requestId": requestID})
	case errors.Is(err, ErrGmailImportConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency key was already used for a different command.", "requestId": requestID})
	case errors.Is(err, ErrLocalValidationConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency key was already used for a different command.", "requestId": requestID})
	case errors.Is(err, ErrLocalResourceVersion):
		c.JSON(http.StatusConflict, gin.H{"message": "Resource changed; refresh and try again.", "requestId": requestID})
	case errors.Is(err, ErrGmailImportDependency), errors.Is(err, ErrGmailImportStorage):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Resource service is temporarily unavailable.", "requestId": requestID})
	case errors.Is(err, ErrLocalValidationDependency):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Resource service is temporarily unavailable.", "requestId": requestID})
	case errors.Is(err, ErrLocalResourceBusy):
		c.JSON(http.StatusConflict, gin.H{"message": "Gmail resource is leased or sold.", "requestId": requestID})
	case errors.Is(err, ErrSessionMissing), errors.Is(err, ErrLocalResourceMissing), errors.Is(err, ErrGmailImportNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
