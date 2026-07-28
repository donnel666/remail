package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	mod *Module
}

func NewHandler(mod *Module) *Handler { return &Handler{mod: mod} }

// GetDashboard returns the signed-in user's console dashboard aggregates over
// the optional [createdFrom, createdTo] window.
func (h *Handler) GetDashboard(c *gin.Context) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return
	}
	h.getDashboard(c, userID)
}

// GetAdminUserDashboard returns the selected user's compact dashboard stats.
func (h *Handler) GetAdminUserDashboard(c *gin.Context) {
	parsed, err := strconv.ParseUint(c.Param("userId"), 10, 64)
	if err != nil || parsed == 0 {
		badRequest(c)
		return
	}
	from, to, ok := dashboardRange(c)
	if !ok {
		badRequest(c)
		return
	}
	result, err := h.mod.Query.ConsoleStats(c.Request.Context(), uint(parsed), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to load dashboard.", "requestId": middleware.GetRequestID(c)})
		return
	}
	c.JSON(http.StatusOK, DashboardStats(*result))
}

func (h *Handler) getDashboard(c *gin.Context, userID uint) {
	from, to, ok := dashboardRange(c)
	if !ok {
		badRequest(c)
		return
	}
	result, err := h.mod.Query.ConsoleDashboard(c.Request.Context(), userID, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to load dashboard.", "requestId": middleware.GetRequestID(c)})
		return
	}
	c.JSON(http.StatusOK, dashboardResponse(result))
}

func dashboardRange(c *gin.Context) (*time.Time, *time.Time, bool) {
	from, ok := parseOptionalTime(c.Query("createdFrom"))
	if !ok {
		return nil, nil, false
	}
	to, ok := parseOptionalTime(c.Query("createdTo"))
	return from, to, ok
}

func badRequest(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
}

func parseOptionalTime(raw string) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}
