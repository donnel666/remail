package icloud

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterUserRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, turnstileGuard gin.HandlerFunc) {
	if rg == nil || module == nil || module.Service == nil {
		return
	}
	h := &handler{service: module.Service}
	routes := rg.Group("/icloud/resources", middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	if turnstileGuard != nil {
		routes.Use(turnstileGuard)
	}
	routes.GET("", h.listOwnedResources)
	routes.POST("/imports", middleware.RateLimitPerUser(module.Service.deviceRedis, "icloud_user_import", 10, 60), h.importOwnedResources)
	routes.GET("/imports/:importId", h.ownedImport)
	routes.POST("/actions", middleware.RateLimitPerUser(module.Service.deviceRedis, "icloud_user_bulk", 20, 60), h.ownedBatch)
	routes.GET("/:resourceId/aliases", h.ownedAliases)
	routes.POST("/:resourceId/validation", h.ownedCommand(AdminICloudValidate))
	routes.POST("/:resourceId/publish", h.ownedCommand(AdminICloudPublish))
	routes.DELETE("/:resourceId", h.ownedCommand(AdminICloudDelete))
}

func ownedICloudUserID(c *gin.Context) (uint, bool) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok || userID == 0 {
		c.AbortWithStatus(http.StatusUnauthorized)
		return 0, false
	}
	return userID, true
}

func ownedICloudPublishAllowed(c *gin.Context, command AdminICloudCommand) bool {
	if command != AdminICloudPublish {
		return true
	}
	role, ok := middleware.GetCurrentRole(c)
	if !ok || !role.HasSupplierAccess() {
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	return true
}

func (h *handler) listOwnedResources(c *gin.Context) {
	userID, ok := ownedICloudUserID(c)
	if !ok {
		return
	}
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if offsetErr != nil || limitErr != nil {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	var forSale *bool
	if value, exists := c.GetQuery("forSale"); exists {
		parsed, err := strconv.ParseBool(value)
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
	result, err := h.service.ListOwnedICloudResources(c.Request.Context(), userID, AdminICloudResourceListFilter{
		Search: c.Query("search"), Suffix: strings.TrimPrefix(strings.TrimSpace(c.Query("suffix")), "@"), Status: c.Query("status"),
		ForSale: forSale, CreatedFrom: createdFrom, CreatedTo: createdTo, Offset: offset, Limit: limit,
	})
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h *handler) importOwnedResources(c *gin.Context) {
	userID, ok := ownedICloudUserID(c)
	if !ok || !requireICloudIdempotencyKey(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, userICloudImportMaxBytes+iCloudImportMultipartBytes)
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		writeICloudError(c, ErrICloudOnboardingInvalid)
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, userICloudImportMaxBytes+1))
	if err != nil || len(content) > userICloudImportMaxBytes {
		writeICloudError(c, ErrICloudOnboardingInvalid)
		return
	}
	view, _, err := h.service.AcceptOwnedICloudImport(c.Request.Context(), userID, content, c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath())
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, view)
}

func (h *handler) ownedImport(c *gin.Context) {
	userID, ok := ownedICloudUserID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("importId"), 10, 64)
	if err != nil || id == 0 {
		writeICloudError(c, ErrICloudOnboardingNotFound)
		return
	}
	view, err := h.service.GetOwnedICloudImport(c.Request.Context(), userID, uint(id))
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, view)
}

func (h *handler) ownedAliases(c *gin.Context) {
	userID, ok := ownedICloudUserID(c)
	if !ok {
		return
	}
	id, idErr := strconv.ParseUint(c.Param("resourceId"), 10, 64)
	offset, offsetErr := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if idErr != nil || id == 0 || offsetErr != nil || limitErr != nil {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	result, err := h.service.ListOwnedICloudAliases(c.Request.Context(), userID, uint(id), offset, limit)
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h *handler) ownedCommand(command AdminICloudCommand) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := ownedICloudUserID(c)
		if !ok || !requireICloudIdempotencyKey(c) || !ownedICloudPublishAllowed(c, command) {
			return
		}
		id, err := strconv.ParseUint(c.Param("resourceId"), 10, 64)
		var input struct {
			Version uint64 `json:"version"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
		if err != nil || id == 0 || c.ShouldBindJSON(&input) != nil || input.Version == 0 {
			writeICloudError(c, ErrICloudResourceQuery)
			return
		}
		result, err := h.service.ApplyOwnedICloudCommand(c.Request.Context(), userID, command, uint(id), input.Version, c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath())
		if err != nil {
			writeICloudError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func (h *handler) ownedBatch(c *gin.Context) {
	userID, ok := ownedICloudUserID(c)
	if !ok || !requireICloudIdempotencyKey(c) {
		return
	}
	var input struct {
		Command   AdminICloudCommand       `json:"command"`
		Resources []OwnedICloudCommandItem `json:"resources"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	if c.ShouldBindJSON(&input) != nil {
		writeICloudError(c, ErrICloudResourceQuery)
		return
	}
	if !ownedICloudPublishAllowed(c, input.Command) {
		return
	}
	result, err := h.service.ApplyOwnedICloudBatch(c.Request.Context(), userID, input.Command, input.Resources, c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath())
	if err != nil {
		writeICloudError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
