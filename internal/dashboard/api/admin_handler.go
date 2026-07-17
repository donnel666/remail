package api

import (
	"net/http"

	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

// GetAdminDashboard returns the platform-wide dashboard aggregates over the
// optional [createdFrom, createdTo] window. Admin-scoped (see RegisterAdminRoutes).
func (h *Handler) GetAdminDashboard(c *gin.Context) {
	if h.mod.AdminQuery == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Dashboard is not available.", "requestId": middleware.GetRequestID(c)})
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
	result, err := h.mod.AdminQuery.AdminDashboard(c.Request.Context(), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to load dashboard.", "requestId": middleware.GetRequestID(c)})
		return
	}
	c.JSON(http.StatusOK, adminDashboardResponse(result))
}
