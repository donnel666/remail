package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/donnel666/remail/api/middleware"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/gin-gonic/gin"
)

type BotLeaderboardRewardItem struct {
	Rank         int    `json:"rank"`
	Name         string `json:"name"`
	SuccessCount int    `json:"successCount"`
	RewardAmount string `json:"rewardAmount"`
}

type BotLeaderboardRewardsResponse struct {
	Available    bool                       `json:"available"`
	BusinessDate *string                    `json:"businessDate"`
	PeriodStart  *time.Time                 `json:"periodStart"`
	PeriodEnd    *time.Time                 `json:"periodEnd"`
	SettledAt    *time.Time                 `json:"settledAt"`
	Items        []BotLeaderboardRewardItem `json:"items"`
}

// RegisterBotRoutes mounts billing-backed bot reads. Authentication and rate
// limiting are applied by the caller to rg.
func RegisterBotRoutes(rg *gin.RouterGroup, mod *BillingModule) {
	h := &BillingHandler{module: mod}
	rg.GET("/recharges/config", h.GetRechargeConfig)
	rg.POST("/recharges/quote", h.PostBotRechargeQuote)
	rg.GET("/rankings/rewards/latest", h.GetBotLatestLeaderboardRewards)
}

// Only the two quote inputs are accepted; no user, scope or identity overrides.
// The existing quote handler owns validation and all pricing calculations.
func (h *BillingHandler) PostBotRechargeQuote(c *gin.Context) {
	invalid := errors.New("only points and paymentMethod are accepted in one bounded JSON object")
	if c.Request.URL.RawQuery != "" {
		writeInvalidBody(c, invalid)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxRechargeRequestBytes))
	if err != nil {
		writeInvalidBody(c, invalid)
		return
	}
	var request RechargeQuoteRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeInvalidBody(c, invalid)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeInvalidBody(c, invalid)
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	h.PostRechargeQuote(c)
}

func (h *BillingHandler) GetBotLatestLeaderboardRewards(c *gin.Context) {
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
			return
		}
		limit = parsed
	}
	if h == nil || h.module == nil || h.module.WalletUseCase == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable.", "requestId": middleware.GetRequestID(c)})
		return
	}
	result, err := h.module.WalletUseCase.LatestBotLeaderboardRewards(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable.", "requestId": middleware.GetRequestID(c)})
		return
	}
	c.JSON(http.StatusOK, botLeaderboardRewardsResponse(result))
}

func botLeaderboardRewardsResponse(result *billingapp.BotLeaderboardRewards) BotLeaderboardRewardsResponse {
	response := BotLeaderboardRewardsResponse{
		Available: result.Available, PeriodStart: result.PeriodStart, PeriodEnd: result.PeriodEnd,
		SettledAt: result.SettledAt, Items: make([]BotLeaderboardRewardItem, len(result.Items)),
	}
	if result.BusinessDate != "" {
		response.BusinessDate = &result.BusinessDate
	}
	for i, item := range result.Items {
		response.Items[i] = BotLeaderboardRewardItem{
			Rank: item.Rank, Name: item.Name, SuccessCount: item.SuccessCount, RewardAmount: item.RewardAmount,
		}
	}
	return response
}
