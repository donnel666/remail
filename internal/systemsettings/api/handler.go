package api

import (
	"errors"
	"net/http"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	module *Module
}

var errUnavailable = errors.New("system settings unavailable")

func NewHandler(module *Module) *Handler { return &Handler{module: module} }

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
	options := make([]settingDTO, len(settings))
	for i := range settings {
		options[i] = toDTO(settings[i])
	}
	c.JSON(http.StatusOK, gin.H{"options": options})
}

func (h *Handler) GetOne(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	setting, err := h.module.Settings.Get(c.Request.Context(), c.Param("key"))
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
	setting, err := h.module.Settings.Upsert(c.Request.Context(), c.Param("key"), *req.Value)
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
	settings := make([]settingDTO, 0, len(req.Settings))
	for _, item := range req.Settings {
		if item.Value == nil {
			badRequest(c)
			return
		}
		setting, err := h.module.Settings.Upsert(c.Request.Context(), item.Key, *item.Value)
		if err != nil {
			writeError(c, err)
			return
		}
		settings = append(settings, toDTO(*setting))
	}
	c.JSON(http.StatusOK, gin.H{"options": settings})
}

func (h *Handler) Delete(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Settings == nil {
		writeError(c, errUnavailable)
		return
	}
	if err := h.module.Settings.Delete(c.Request.Context(), c.Param("key")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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
