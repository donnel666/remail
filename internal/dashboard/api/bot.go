package api

import (
	"net/http"
	"strconv"

	"github.com/donnel666/remail/api/middleware"
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/gin-gonic/gin"
)

type BotLeaderboardItem struct {
	Rank         int    `json:"rank"`
	Name         string `json:"name"`
	SuccessCount int    `json:"successCount"`
}

type BotLeaderboardsResponse struct {
	BusinessDate string               `json:"businessDate"`
	Timezone     string               `json:"timezone"`
	Today        []BotLeaderboardItem `json:"today"`
	Historical   []BotLeaderboardItem `json:"historical"`
}

// RegisterBotRoutes mounts dashboard-backed public bot read models. The
// caller applies bot System Key authentication and rate limiting to rg.
func RegisterBotRoutes(rg *gin.RouterGroup, mod *Module) {
	h := &Handler{mod: mod}
	rg.GET("/rankings/orders", h.GetBotOrderRankings)
}

func (h *Handler) GetBotOrderRankings(c *gin.Context) {
	limit := 10
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
			return
		}
		limit = parsed
	}
	if h == nil || h.mod == nil || h.mod.Query == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable.", "requestId": middleware.GetRequestID(c)})
		return
	}
	result, err := h.mod.Query.BotLeaderboards(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable.", "requestId": middleware.GetRequestID(c)})
		return
	}
	c.JSON(http.StatusOK, botLeaderboardsResponse(result))
}

func botLeaderboardsResponse(result *dashboardapp.BotLeaderboards) BotLeaderboardsResponse {
	return BotLeaderboardsResponse{
		BusinessDate: result.BusinessDate,
		Timezone:     result.Timezone,
		Today:        botLeaderboardItems(result.Today),
		Historical:   botLeaderboardItems(result.Historical),
	}
}

func botLeaderboardItems(rows []dashboardapp.BotRankItem) []BotLeaderboardItem {
	items := make([]BotLeaderboardItem, len(rows))
	for i, row := range rows {
		items[i] = BotLeaderboardItem{Rank: row.Rank, Name: row.Name, SuccessCount: row.SuccessCount}
	}
	return items
}
