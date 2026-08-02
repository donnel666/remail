package gmail

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type Module struct {
	Service *Service
}

func NewModule(db *gorm.DB, queue *asynq.Client) *Module {
	return &Module{Service: NewService(db, queue)}
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
	resources.POST("/import", middleware.PermissionRequired(checker, "core:resource", "write"), h.importLocalResources)
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

type localResourceImportRequest struct {
	Content       string `json:"content" binding:"required"`
	ErrorStrategy string `json:"errorStrategy"`
}

func (h *handler) importLocalResources(c *gin.Context) {
	if c.Request.ContentLength > localResourceImportBodyMaxBytes {
		writeLocalResourceImportTooLarge(c)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, localResourceImportBodyMaxBytes)
	var req localResourceImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeLocalResourceImportTooLarge(c)
			return
		}
		writeGmailError(c, ErrInvalidLocalResource)
		return
	}
	ownerUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	result, err := h.service.ImportLocalResources(c.Request.Context(), ownerUserID, req.Content, req.ErrorStrategy)
	if err != nil {
		writeGmailError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func writeLocalResourceImportTooLarge(c *gin.Context) {
	c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "Gmail resource import is too large.", "requestId": middleware.GetRequestID(c)})
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
	case errors.Is(err, ErrLocalResourceBusy):
		c.JSON(http.StatusConflict, gin.H{"message": "Gmail resource is leased or sold.", "requestId": requestID})
	case errors.Is(err, ErrRouteNotFound), errors.Is(err, ErrSessionMissing), errors.Is(err, ErrLocalResourceMissing):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
