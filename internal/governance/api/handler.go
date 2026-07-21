package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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

func (h *Handler) GetAdminSystemLogs(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Logs == nil {
		writeAdminLogError(c, governanceapp.ErrAdminLogUnavailable)
		return
	}
	filter, ok := parseAdminLogFilter(c)
	if !ok {
		return
	}
	filter.Level = strings.TrimSpace(c.Query("level"))
	result, err := h.module.Logs.ListSystem(c.Request.Context(), filter)
	if err != nil {
		writeAdminLogError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminSystemLogListDTO(result))
}

func (h *Handler) GetAdminOperationLogs(c *gin.Context) {
	if h == nil || h.module == nil || h.module.Logs == nil {
		writeAdminLogError(c, governanceapp.ErrAdminLogUnavailable)
		return
	}
	filter, ok := parseAdminLogFilter(c)
	if !ok {
		return
	}
	filter.Result = strings.TrimSpace(c.Query("result"))
	result, err := h.module.Logs.ListOperations(c.Request.Context(), filter)
	if err != nil {
		writeAdminLogError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminOperationLogListDTO(result))
}

func (h *Handler) DeleteAdminSystemLogs(c *gin.Context) {
	h.deleteAdminLogs(c, governanceapp.AdminLogCategorySystem)
}

func (h *Handler) DeleteAdminOperationLogs(c *gin.Context) {
	h.deleteAdminLogs(c, governanceapp.AdminLogCategoryOperation)
}

func (h *Handler) deleteAdminLogs(c *gin.Context, category string) {
	if h == nil || h.module == nil || h.module.Logs == nil {
		writeAdminLogError(c, governanceapp.ErrAdminLogUnavailable)
		return
	}
	before, ok := parseRequiredAdminLogTime(c, "before")
	if !ok {
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		writeAdminLogError(c, governanceapp.ErrInvalidAdminLogQuery)
		return
	}
	removed, err := h.module.Logs.Cleanup(c.Request.Context(), governanceapp.AdminLogCleanupCommand{
		Category: category, Before: before, OperatorUserID: operatorUserID,
		Path: c.FullPath(), RequestID: middleware.GetRequestID(c),
	})
	if err != nil {
		writeAdminLogError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminLogCleanupResponse{Removed: removed})
}

func parseAdminLogFilter(c *gin.Context) (governanceapp.AdminLogListFilter, bool) {
	offset, ok := parseAdminLogInt(c, "offset", 0, 0, int(^uint(0)>>1))
	if !ok {
		return governanceapp.AdminLogListFilter{}, false
	}
	limit, ok := parseAdminLogInt(c, "limit", governanceapp.AdminLogDefaultLimit, 1, governanceapp.AdminLogMaxLimit)
	if !ok {
		return governanceapp.AdminLogListFilter{}, false
	}
	from, ok := parseOptionalAdminLogTime(c, "from")
	if !ok {
		return governanceapp.AdminLogListFilter{}, false
	}
	to, ok := parseOptionalAdminLogTime(c, "to")
	if !ok {
		return governanceapp.AdminLogListFilter{}, false
	}
	return governanceapp.AdminLogListFilter{
		Search: strings.TrimSpace(c.Query("search")), From: from, To: to, Offset: offset, Limit: limit,
	}, true
}

func parseAdminLogInt(c *gin.Context, name string, defaultValue, minimum, maximum int) (int, bool) {
	raw, exists := c.GetQuery(name)
	if !exists || strings.TrimSpace(raw) == "" {
		return defaultValue, true
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minimum || value > maximum {
		writeAdminLogError(c, governanceapp.ErrInvalidAdminLogQuery)
		return 0, false
	}
	return value, true
}

func parseOptionalAdminLogTime(c *gin.Context, name string) (*time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, true
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeAdminLogError(c, governanceapp.ErrInvalidAdminLogQuery)
		return nil, false
	}
	return &value, true
}

func parseRequiredAdminLogTime(c *gin.Context, name string) (time.Time, bool) {
	value, ok := parseOptionalAdminLogTime(c, name)
	if !ok || value == nil {
		if ok {
			writeAdminLogError(c, governanceapp.ErrInvalidAdminLogQuery)
		}
		return time.Time{}, false
	}
	return *value, true
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

func writeAdminLogError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, governanceapp.ErrInvalidAdminLogQuery):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, governanceapp.ErrAdminLogUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
