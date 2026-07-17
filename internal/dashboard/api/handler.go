package api

import (
	"net/http"
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
	from, ok := parseOptionalTime(c.Query("createdFrom"))
	if !ok {
		badRequest(c)
		return
	}
	to, ok := parseOptionalTime(c.Query("createdTo"))
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
