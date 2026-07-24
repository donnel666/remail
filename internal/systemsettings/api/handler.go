package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/systemsettings/app"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	module  *Module
	checker middleware.PermissionChecker
}

var errUnavailable = errors.New("system settings unavailable")

func NewHandler(module *Module, checker middleware.PermissionChecker) *Handler {
	return &Handler{module: module, checker: checker}
}

type settingDTO struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type settingValueRequest struct {
	Value *string `json:"value"`
}

type bulkSettingRequest struct {
	Settings []bulkSettingItem `json:"settings"`
}

type bulkSettingItem struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

func (h *Handler) Get(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	settings, err := h.module.Settings.List(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	canReadSensitive, ok := h.sensitiveAllowed(c)
	if !ok {
		return
	}
	options := make([]settingDTO, 0, len(settings))
	for i := range settings {
		if isSensitiveKey(settings[i].Key) && !canReadSensitive {
			continue
		}
		options = append(options, toDTO(settings[i]))
	}
	c.JSON(http.StatusOK, gin.H{"options": options})
}

func (h *Handler) GetOne(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	key := c.Param("key")
	if !h.requireSensitive(c, key) {
		return
	}
	setting, err := h.module.Settings.Get(c.Request.Context(), key)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"option": toDTO(*setting)})
}

func (h *Handler) Put(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	var req settingValueRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Value == nil {
		badRequest(c)
		return
	}
	key := c.Param("key")
	if !h.requireSensitive(c, key) {
		return
	}
	setting, err := h.module.Settings.Upsert(c.Request.Context(), key, *req.Value, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"option": toDTO(*setting)})
}

func (h *Handler) PutBulk(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	var req bulkSettingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c)
		return
	}
	updates := make([]domain.Setting, 0, len(req.Settings))
	sensitiveKey := ""
	for _, item := range req.Settings {
		if item.Value == nil {
			badRequest(c)
			return
		}
		if isSensitiveKey(item.Key) {
			sensitiveKey = item.Key
		}
		updates = append(updates, domain.Setting{Key: item.Key, Value: *item.Value})
	}
	if sensitiveKey != "" && !h.requireSensitive(c, sensitiveKey) {
		return
	}
	settings, err := h.module.Settings.BulkUpsert(c.Request.Context(), updates, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	options := make([]settingDTO, len(settings))
	for i := range settings {
		options[i] = toDTO(settings[i])
	}
	c.JSON(http.StatusOK, gin.H{"options": options})
}

func (h *Handler) Delete(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	key := c.Param("key")
	if !h.requireSensitive(c, key) {
		return
	}
	if err := h.module.Settings.Delete(c.Request.Context(), key, mutationMeta(c)); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func isSensitiveKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "github_client_secret", "epay_merchant_key":
		return true
	default:
		return false
	}
}

func (h *Handler) requireSensitive(c *gin.Context, key string) bool {
	if !isSensitiveKey(key) {
		return true
	}
	allowed, ok := h.sensitiveAllowed(c)
	if !ok {
		return false
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{
			"message":   "Permission denied.",
			"requestId": middleware.GetRequestID(c),
		})
		return false
	}
	return true
}

func (h *Handler) sensitiveAllowed(c *gin.Context) (bool, bool) {
	userID, userOK := middleware.GetCurrentUserID(c)
	role, roleOK := middleware.GetCurrentRole(c)
	if !userOK || !roleOK {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return false, false
	}
	if h.checker == nil {
		writeError(c, errUnavailable)
		return false, false
	}
	allowed, err := h.checker.Check(c.Request.Context(), userID, role, "system:settings", "sensitive")
	if err != nil {
		writeError(c, err)
		return false, false
	}
	return allowed, true
}

func mutationMeta(c *gin.Context) app.MutationMeta {
	operatorID, _ := middleware.GetCurrentUserID(c)
	path := ""
	if c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	return app.MutationMeta{
		OperatorUserID: operatorID,
		RequestID:      middleware.GetRequestID(c),
		Path:           path,
	}
}

func toDTO(setting domain.Setting) settingDTO {
	return settingDTO{
		Key:       setting.Key,
		Value:     setting.Value,
		CreatedAt: setting.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		UpdatedAt: setting.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

func badRequest(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"message":   "Invalid request body.",
		"requestId": middleware.GetRequestID(c),
	})
}

func writeError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "An unexpected error occurred."
	switch {
	case errors.Is(err, domain.ErrInvalidKey):
		status, message = http.StatusBadRequest, "Invalid system setting key."
	case errors.Is(err, domain.ErrSettingNotFound):
		status, message = http.StatusNotFound, "System setting not found."
	case errors.Is(err, errUnavailable):
		status, message = http.StatusServiceUnavailable, "System settings are unavailable."
	}
	c.JSON(status, gin.H{"message": message, "requestId": middleware.GetRequestID(c)})
}
