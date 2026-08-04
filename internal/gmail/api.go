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
	admin := rg.Group("/admin/upstreams/smsbower")
	admin.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	admin.GET("/status", middleware.PermissionRequired(checker, "system:settings", "read"), h.status)
	admin.POST("/sync", middleware.PermissionRequired(checker, "system:settings", "write"), h.sync)
	admin.GET("/services", middleware.PermissionRequired(checker, "system:settings", "read"), h.services)
	admin.GET("/mappings", middleware.PermissionRequired(checker, "system:settings", "read"), h.mappings)
	admin.PUT("/mappings/:projectId", middleware.PermissionRequired(checker, "system:settings", "write"), h.putMapping)
	admin.DELETE("/mappings/:projectId", middleware.PermissionRequired(checker, "system:settings", "write"), h.deleteMapping)
	admin.GET("/finance", middleware.PermissionRequired(checker, "billing:wallet", "read"), h.finance)
	admin.GET("/activations", middleware.PermissionRequired(checker, "billing:wallet", "read"), h.activations)

	resources := rg.Group("/admin/gmail/resources")
	resources.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	resources.GET("", middleware.PermissionRequired(checker, "core:resource", "read"), h.localResources)
	resources.POST("/imports", middleware.PermissionRequired(checker, "core:resource", "write"), h.importLocalResources)
	resources.GET("/imports/:importId", middleware.PermissionRequired(checker, "core:resource", "read"), h.localResourceImport)
	resources.POST("/:resourceId/enable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.enableLocalResource)
	resources.POST("/:resourceId/disable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.disableLocalResource)
}

type handler struct{ service *Service }

func (h *handler) status(c *gin.Context) {
	status, err := h.service.AccountStatus(c.Request.Context())
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, status)
}

func (h *handler) sync(c *gin.Context) {
	if err := h.service.ScheduleSync(c.Request.Context()); err != nil {
		writeGmailError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

func (h *handler) services(c *gin.Context) {
	items, err := h.service.ListServices(c.Request.Context())
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *handler) mappings(c *gin.Context) {
	items, err := h.service.ListMappings(c.Request.Context())
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type mappingRequest struct {
	Source              string `json:"source" binding:"required"`
	ProviderServiceCode string `json:"providerServiceCode"`
	Enabled             *bool  `json:"enabled" binding:"required"`
	CodeEnabled         *bool  `json:"codeEnabled" binding:"required"`
	PurchaseEnabled     *bool  `json:"purchaseEnabled" binding:"required"`
}

func (h *handler) putMapping(c *gin.Context) {
	projectID, err := strconv.ParseUint(strings.TrimSpace(c.Param("projectId")), 10, 64)
	var req mappingRequest
	if err != nil || projectID == 0 || c.ShouldBindJSON(&req) != nil || req.Enabled == nil || req.CodeEnabled == nil || req.PurchaseEnabled == nil {
		writeGmailError(c, ErrInvalidRoute)
		return
	}
	if err := h.service.PutMapping(c.Request.Context(), uint(projectID), req.Source, req.ProviderServiceCode, *req.Enabled, *req.CodeEnabled, *req.PurchaseEnabled); err != nil {
		writeGmailError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) deleteMapping(c *gin.Context) {
	projectID, err := strconv.ParseUint(strings.TrimSpace(c.Param("projectId")), 10, 64)
	if err != nil || projectID == 0 {
		writeGmailError(c, ErrInvalidRoute)
		return
	}
	if err := h.service.DeleteMapping(c.Request.Context(), uint(projectID), c.Query("source")); err != nil {
		writeGmailError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) finance(c *gin.Context) {
	report, err := h.service.Finance(c.Request.Context())
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h *handler) activations(c *gin.Context) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	items, total, err := h.service.ListActivations(c.Request.Context(), offset, limit)
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "offset": offset, "limit": limit})
}

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
	resourceID, err := strconv.ParseUint(strings.TrimSpace(c.Param("resourceId")), 10, 64)
	if err != nil || resourceID == 0 {
		writeGmailError(c, ErrLocalResourceMissing)
		return
	}
	if err := h.service.SetLocalResourceEnabled(c.Request.Context(), uint(resourceID), enabled); err != nil {
		writeGmailError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func writeGmailError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, ErrInvalidRoute):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid Gmail upstream mapping.", "requestId": requestID})
	case errors.Is(err, ErrInvalidLocalResource):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid Gmail resource input.", "requestId": requestID})
	case errors.Is(err, ErrGmailImportInvalidCommand), errors.Is(err, ErrGmailImportInvalidOwner):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid resource command.", "requestId": requestID})
	case errors.Is(err, ErrGmailImportConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency key was already used for a different command.", "requestId": requestID})
	case errors.Is(err, ErrGmailImportDependency), errors.Is(err, ErrGmailImportStorage):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Resource service is temporarily unavailable.", "requestId": requestID})
	case errors.Is(err, ErrLocalResourceBusy):
		c.JSON(http.StatusConflict, gin.H{"message": "Gmail resource is leased or sold.", "requestId": requestID})
	case errors.Is(err, ErrRouteNotFound), errors.Is(err, ErrSessionMissing), errors.Is(err, ErrLocalResourceMissing), errors.Is(err, ErrGmailImportNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
