package api

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/systemsettings/app"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
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

func (h *Handler) GetAnnouncements(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit < 1 || limit > 100 || len(c.QueryArray("limit")) > 1 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters."})
		return
	}
	items := runtimeconfig.ActiveAnnouncements(time.Now(), limit+1)
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	c.JSON(http.StatusOK, gin.H{"announcements": items, "truncated": truncated})
}

func (h *Handler) GetNotice(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"notice": strings.TrimSpace(runtimeconfig.String("global_notice", ""))})
}

func (h *Handler) GetFAQs(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit < 1 || limit > 100 || len(c.QueryArray("limit")) > 1 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters."})
		return
	}
	enabled, items := runtimeconfig.PublicFAQs(limit + 1)
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	c.JSON(http.StatusOK, gin.H{"enabled": enabled, "items": items, "truncated": truncated})
}

func (h *Handler) GetCustomerService(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"qqGroupNumber":    strings.TrimSpace(runtimeconfig.String("customer_service_qq_group_number", "")),
		"qqGroupUrl":       strings.TrimSpace(runtimeconfig.String("customer_service_qq_group_url", "")),
		"telegramGroupUrl": strings.TrimSpace(runtimeconfig.String("customer_service_telegram_group_url", "")),
	})
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

type systemKeyRequest struct {
	Name             string                  `json:"name"`
	Purpose          domain.SystemKeyPurpose `json:"purpose"`
	Platform         string                  `json:"platform"`
	SubjectNamespace string                  `json:"subjectNamespace"`
	AllowedGroupIDs  []string                `json:"allowedGroupIds"`
}

type systemKeyDTO struct {
	ID               uint       `json:"id"`
	Name             string     `json:"name"`
	Purpose          string     `json:"purpose"`
	Platform         string     `json:"platform,omitempty"`
	SubjectNamespace string     `json:"subjectNamespace,omitempty"`
	AllowedGroupIDs  []string   `json:"allowedGroupIds,omitempty"`
	KeyPrefix        string     `json:"keyPrefix"`
	KeyPlain         string     `json:"keyPlain,omitempty"`
	LastUsedAt       *time.Time `json:"lastUsedAt"`
	CreatedAt        time.Time  `json:"createdAt"`
}

func (h *Handler) GetSystemKeys(c *gin.Context) {
	if h == nil || h.module == nil || h.module.SystemKeys == nil {
		writeSystemKeyError(c, errUnavailable)
		return
	}
	keys, err := h.module.SystemKeys.List(c.Request.Context())
	if err != nil {
		writeSystemKeyError(c, err)
		return
	}
	items := make([]systemKeyDTO, len(keys))
	for i := range keys {
		items[i] = toSystemKeyDTO(keys[i], false)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) PostSystemKey(c *gin.Context) {
	if h == nil || h.module == nil || h.module.SystemKeys == nil {
		writeSystemKeyError(c, errUnavailable)
		return
	}
	var req systemKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeSystemKeyError(c, domain.ErrInvalidSystemKey)
		return
	}
	if req.Purpose == "" {
		req.Purpose = domain.SystemKeyPurposeICloudForwarding
	}
	key, err := h.module.SystemKeys.CreateWithScope(
		c.Request.Context(), req.Name, req.Purpose, req.Platform, req.SubjectNamespace,
		mutationMeta(c), req.AllowedGroupIDs...,
	)
	if err != nil {
		writeSystemKeyError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toSystemKeyDTO(*key, true))
}

func (h *Handler) DeleteSystemKey(c *gin.Context) {
	if h == nil || h.module == nil || h.module.SystemKeys == nil {
		writeSystemKeyError(c, errUnavailable)
		return
	}
	keyID, err := strconv.ParseUint(strings.TrimSpace(c.Param("keyId")), 10, 64)
	if err != nil || keyID == 0 {
		writeSystemKeyError(c, domain.ErrInvalidSystemKey)
		return
	}
	if err := h.module.SystemKeys.Delete(c.Request.Context(), uint(keyID), mutationMeta(c)); err != nil {
		writeSystemKeyError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func toSystemKeyDTO(key domain.SystemKey, includePlain bool) systemKeyDTO {
	dto := systemKeyDTO{
		ID: key.ID, Name: key.Name, Purpose: string(key.Purpose), Platform: key.Platform,
		SubjectNamespace: key.SubjectNamespace, AllowedGroupIDs: key.AllowedGroupIDs, KeyPrefix: key.KeyPrefix,
		LastUsedAt: key.LastUsedAt, CreatedAt: key.CreatedAt,
	}
	if includePlain {
		dto.KeyPlain = key.KeyPlain
	}
	return dto
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
		if isWriteOnlyKey(settings[i].Key) || isSensitiveKey(settings[i].Key) && !canReadSensitive {
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
	if isWriteOnlyKey(key) {
		writeError(c, domain.ErrSettingNotFound)
		return
	}
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
	if !h.requireSensitiveMutation(c, key) {
		return
	}
	setting, err := h.module.Settings.Upsert(c.Request.Context(), key, *req.Value, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	if isWriteOnlyKey(key) {
		c.Status(http.StatusNoContent)
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
		if isSensitiveMutationKey(item.Key) {
			sensitiveKey = item.Key
		}
		updates = append(updates, domain.Setting{Key: item.Key, Value: *item.Value})
	}
	canReadSensitive := false
	if sensitiveKey != "" {
		if !h.requireSensitiveMutation(c, sensitiveKey) {
			return
		}
		canReadSensitive = true
	}
	settings, err := h.module.Settings.BulkUpsert(c.Request.Context(), updates, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	options := make([]settingDTO, 0, len(settings))
	for i := range settings {
		if isWriteOnlyKey(settings[i].Key) || (isSensitiveKey(settings[i].Key) && !canReadSensitive) {
			continue
		}
		options = append(options, toDTO(settings[i]))
	}
	c.JSON(http.StatusOK, gin.H{"options": options})
}

func (h *Handler) Delete(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	key := c.Param("key")
	if !h.requireSensitiveMutation(c, key) {
		return
	}
	if err := h.module.Settings.Delete(c.Request.Context(), key, mutationMeta(c)); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func isSensitiveKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "github_client_id", "github_client_secret", "github_callback_url",
		"linuxdo_client_id", "linuxdo_client_secret", "linuxdo_callback_url",
		"nodeloc_client_id", "nodeloc_client_secret", "nodeloc_callback_url",
		"epay_enabled", "epay_version", "epay_gateway_url", "epay_merchant_id", "epay_merchant_key", "epay_private_key", "epay_platform_public_key", "epay_notify_url", "epay_return_url",
		"epusdt_enabled", "epusdt_gateway_url", "epusdt_pid", "epusdt_api_key", "epusdt_api_secret", "epusdt_token", "epusdt_network", "epusdt_notify_url", "epusdt_return_url", "epusdt_allowed_hosts":
		return true
	default:
		return false
	}
}

func isWriteOnlyKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "epay_merchant_key", "epay_private_key", "epusdt_api_key", "epusdt_api_secret", "github_client_id", "github_client_secret", "linuxdo_client_id", "linuxdo_client_secret", "nodeloc_client_id", "nodeloc_client_secret", "points_unit_migration_v1":
		return true
	default:
		return false
	}
}

func isSensitiveMutationKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "github_client_id", "github_callback_url", "linuxdo_client_id", "linuxdo_callback_url", "nodeloc_client_id", "nodeloc_callback_url":
		return true
	default:
		return isSensitiveKey(key)
	}
}

func (h *Handler) requireSensitive(c *gin.Context, key string) bool {
	return h.requireSensitiveWhen(c, isSensitiveKey(key))
}

func (h *Handler) requireSensitiveMutation(c *gin.Context, key string) bool {
	return h.requireSensitiveWhen(c, isSensitiveMutationKey(key))
}

func (h *Handler) requireSensitiveWhen(c *gin.Context, required bool) bool {
	if !required {
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
		Key:       strings.ToLower(strings.TrimSpace(setting.Key)),
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
	case errors.Is(err, domain.ErrInvalidValue):
		status, message = http.StatusBadRequest, "Invalid system setting value."
	case errors.Is(err, domain.ErrSettingNotFound):
		status, message = http.StatusNotFound, "System setting not found."
	case errors.Is(err, errUnavailable):
		status, message = http.StatusServiceUnavailable, "System settings are unavailable."
	}
	body := gin.H{"message": message, "requestId": middleware.GetRequestID(c)}
	var fieldError *domain.InvalidValueFieldsError
	if errors.As(err, &fieldError) && len(fieldError.Fields) > 0 {
		keys := make([]string, 0, len(fieldError.Fields))
		for key := range fieldError.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		body["message"] = "Invalid system setting value: " + strings.Join(keys, ", ") + "."
		body["fields"] = fieldError.Fields
	}
	c.JSON(status, body)
}

func writeSystemKeyError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "An unexpected error occurred."
	switch {
	case errors.Is(err, domain.ErrInvalidSystemKey):
		status, message = http.StatusBadRequest, "Invalid system key."
	case errors.Is(err, domain.ErrSystemKeyNotFound):
		status, message = http.StatusNotFound, "System key not found."
	case errors.Is(err, errUnavailable):
		status, message = http.StatusServiceUnavailable, "System keys are unavailable."
	}
	c.JSON(status, gin.H{"message": message, "requestId": middleware.GetRequestID(c)})
}
