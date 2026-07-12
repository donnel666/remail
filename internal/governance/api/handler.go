package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	module *Module
}

func NewHandler(module *Module) *Handler {
	return &Handler{module: module}
}

func (h *Handler) GetAdminTasks(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Tasks == nil {
		writeAdminTaskError(c, governanceapp.ErrAdminTaskUnavailable)
		return
	}
	bizID, ok := parseRequiredAdminTaskUint(c, "bizId")
	if !ok {
		return
	}
	offset, ok := parseAdminTaskInt(c, "offset", 0, 0, int(^uint(0)>>1))
	if !ok {
		return
	}
	limit, ok := parseAdminTaskInt(c, "limit", governanceapp.AdminTaskDefaultLimit, 1, governanceapp.AdminTaskMaxLimit)
	if !ok {
		return
	}
	result, err := h.module.Tasks.List(c.Request.Context(), governanceapp.AdminTaskListFilter{
		BizType: strings.TrimSpace(c.Query("bizType")),
		BizID:   bizID,
		Kind:    strings.TrimSpace(c.Query("kind")),
		Status:  strings.TrimSpace(c.Query("status")),
		Offset:  offset,
		Limit:   limit,
	})
	if err != nil {
		writeAdminTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminTaskListDTO(result))
}

func (h *Handler) GetAdminTask(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Tasks == nil {
		writeAdminTaskError(c, governanceapp.ErrAdminTaskUnavailable)
		return
	}
	task, err := h.module.Tasks.Get(c.Request.Context(), strings.TrimSpace(c.Param("taskId")))
	if err != nil {
		writeAdminTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminTaskDTO(*task))
}

func parseRequiredAdminTaskUint(c *gin.Context, name string) (uint, bool) {
	raw, exists := c.GetQuery(name)
	if !exists {
		writeAdminTaskError(c, governanceapp.ErrInvalidAdminTaskQuery)
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 0)
	if err != nil || value == 0 {
		writeAdminTaskError(c, governanceapp.ErrInvalidAdminTaskQuery)
		return 0, false
	}
	return uint(value), true
}

func parseAdminTaskInt(c *gin.Context, name string, defaultValue, minimum, maximum int) (int, bool) {
	raw, exists := c.GetQuery(name)
	if !exists || strings.TrimSpace(raw) == "" {
		return defaultValue, true
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minimum || value > maximum {
		writeAdminTaskError(c, governanceapp.ErrInvalidAdminTaskQuery)
		return 0, false
	}
	return value, true
}

func writeAdminTaskError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, governanceapp.ErrInvalidAdminTaskQuery):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, governanceapp.ErrAdminTaskResourceGone):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	case errors.Is(err, governanceapp.ErrAdminTaskNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Task not found.", "requestId": requestID})
	case errors.Is(err, governanceapp.ErrAdminTaskUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
