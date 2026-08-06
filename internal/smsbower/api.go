package smsbower

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type Module struct{ Service *Service }

func NewModule(db *gorm.DB, queue *asynq.Client) *Module {
	service := NewService(db, queue)
	service.SetOperationLogs(governanceinfra.NewOperationLogRepo(db))
	return &Module{Service: service}
}

func RegisterRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := &handler{service: module.Service, checker: checker}
	admin := rg.Group("/admin/upstreams/smsbower")
	admin.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	admin.GET("/config", middleware.PermissionRequired(checker, "system:settings", "read"), h.config)
	admin.PUT("/config", middleware.PermissionRequired(checker, "system:settings", "write"), h.putConfig)
	admin.GET("/status", middleware.PermissionRequired(checker, "system:settings", "read"), h.status)
	admin.POST("/sync", middleware.PermissionRequired(checker, "system:settings", "write"), h.sync)
	admin.GET("/services", middleware.PermissionRequired(checker, "system:settings", "read"), h.services)
	admin.GET("/mappings", middleware.PermissionRequired(checker, "system:settings", "read"), h.mappings)
	admin.PUT("/mappings/:projectId", middleware.PermissionRequired(checker, "system:settings", "write"), h.putMapping)
	admin.DELETE("/mappings/:projectId", middleware.PermissionRequired(checker, "system:settings", "write"), h.deleteMapping)
	admin.GET("/finance", middleware.PermissionRequired(checker, "billing:wallet", "read"), h.finance)
	admin.GET("/activations", middleware.PermissionRequired(checker, "billing:wallet", "read"), h.activations)
}

type handler struct {
	service *Service
	checker middleware.PermissionChecker
}

func (h *handler) config(c *gin.Context) {
	config, err := h.service.GetConfig(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, config)
}

func (h *handler) putConfig(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var request ConfigUpdate
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, ErrInvalidConfig)
		return
	}
	if strings.TrimSpace(request.APIKey) != "" && !h.canWriteSensitive(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
		return
	}
	config, err := h.service.UpdateConfig(c.Request.Context(), request, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	if err := h.service.ScheduleSync(c.Request.Context()); err != nil {
		slog.Warn("schedule SMSBower sync after config update failed", "request_id", middleware.GetRequestID(c), "error", err)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, config)
}

func (h *handler) canWriteSensitive(c *gin.Context) bool {
	userID, userOK := middleware.GetCurrentUserID(c)
	role, roleOK := middleware.GetCurrentRole(c)
	if !userOK || !roleOK || h.checker == nil {
		return false
	}
	allowed, err := h.checker.Check(c.Request.Context(), userID, role, "system:settings", "sensitive")
	return err == nil && allowed
}

func (h *handler) status(c *gin.Context) {
	status, err := h.service.AccountStatus(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, status)
}

func (h *handler) sync(c *gin.Context) {
	if err := h.service.ScheduleSync(c.Request.Context()); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

func (h *handler) services(c *gin.Context) {
	items, err := h.service.ListServices(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *handler) mappings(c *gin.Context) {
	items, err := h.service.ListMappings(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type mappingRequest struct {
	ProviderServiceCode string `json:"providerServiceCode" binding:"required"`
	Enabled             *bool  `json:"enabled"`
}

func (h *handler) putMapping(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	projectID, err := strconv.ParseUint(strings.TrimSpace(c.Param("projectId")), 10, 64)
	var request mappingRequest
	if err != nil || projectID == 0 || c.ShouldBindJSON(&request) != nil {
		writeError(c, ErrInvalidRoute)
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	if err := h.service.PutMapping(c.Request.Context(), uint(projectID), request.ProviderServiceCode, enabled, mutationMeta(c)); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) deleteMapping(c *gin.Context) {
	projectID, err := strconv.ParseUint(strings.TrimSpace(c.Param("projectId")), 10, 64)
	if err != nil || projectID == 0 {
		writeError(c, ErrInvalidRoute)
		return
	}
	if err := h.service.DeleteMapping(c.Request.Context(), uint(projectID), mutationMeta(c)); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func mutationMeta(c *gin.Context) MutationMeta {
	operatorID, _ := middleware.GetCurrentUserID(c)
	path := ""
	if c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	return MutationMeta{OperatorUserID: operatorID, RequestID: middleware.GetRequestID(c), Path: path}
}

func (h *handler) finance(c *gin.Context) {
	report, err := h.service.Finance(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

func (h *handler) activations(c *gin.Context) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset = max(offset, 0)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	items, total, err := h.service.ListActivations(c.Request.Context(), offset, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "offset": offset, "limit": limit})
}

func writeError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, ErrInvalidConfig), errors.Is(err, ErrInvalidRoute):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid SMSBower configuration.", "requestId": requestID})
	case errors.Is(err, ErrRouteNotFound), errors.Is(err, ErrOrderMissing):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
