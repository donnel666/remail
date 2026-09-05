package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapi "github.com/donnel666/remail/internal/mailmatch/api"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/gin-gonic/gin"
)

type botOrderSummary struct {
	ProjectID      uint       `json:"projectId"`
	ProjectName    string     `json:"projectName"`
	ProductType    string     `json:"productType"`
	ServiceMode    string     `json:"serviceMode"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"createdAt"`
	ActivatedAt    *time.Time `json:"activatedAt,omitempty"`
	ReceiveUntil   *time.Time `json:"receiveUntil,omitempty"`
	AfterSaleUntil *time.Time `json:"afterSaleUntil,omitempty"`
}

type botOrderList func(context.Context, tradeapp.OrderListFilter, int, uint, int) (*tradeapp.OrderListResult, error)

// The bot gets a private, owner-scoped projection, never ordinary order DTOs.
func getBotOrders(c *gin.Context, resolve mailmatchapi.BotUserIDResolver, list botOrderList) {
	identity, ok := middleware.GetCurrentBotIdentity(c)
	if !ok || identity.Scene != middleware.BotScenePrivate {
		c.JSON(http.StatusForbidden, gin.H{"message": "订单列表仅限本人私聊查询。"})
		return
	}
	offset, limit := 0, 100
	for key, values := range c.Request.URL.Query() {
		if (key != "offset" && key != "limit") || len(values) != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters."})
			return
		}
		value, err := strconv.Atoi(values[0])
		if err != nil || (key == "offset" && (value < 0 || value > 10000)) || (key == "limit" && (value < 1 || value > 100)) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters."})
			return
		}
		if key == "offset" {
			offset = value
		} else {
			limit = value
		}
	}
	if resolve == nil || list == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "订单信息暂时不可用。"})
		return
	}
	user, ok := resolve(c)
	if c.IsAborted() || c.Writer.Written() {
		return
	}
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "订单信息暂时不可用。"})
		return
	}
	if !user.Bound || !user.Available || user.UserID == 0 {
		c.JSON(http.StatusOK, gin.H{
			"available": false, "bindingRequired": !user.Bound,
			"accountUnavailable": user.Bound, "items": []botOrderSummary{},
			"offset": offset, "limit": limit, "total": 0, "truncated": false,
		})
		return
	}
	result, err := list(c.Request.Context(), tradeapp.OrderListFilter{UserID: user.UserID, Scope: "mine", IsAdmin: false}, offset, 0, limit)
	if err != nil || result == nil || len(result.Items) > limit {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "订单信息暂时不可用。"})
		return
	}
	items := make([]botOrderSummary, 0, len(result.Items))
	for _, item := range result.Items {
		order := item.Order
		if order.UserID != user.UserID {
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "订单信息暂时不可用。"})
			return
		}
		items = append(items, botOrderSummary{
			ProjectID: order.ProjectID, ProjectName: item.ProjectName,
			ProductType: string(order.ProductType), ServiceMode: string(order.ServiceMode),
			Status: string(order.Status), CreatedAt: order.CreatedAt,
			ActivatedAt: order.ActivatedAt, ReceiveUntil: order.ReceiveUntil, AfterSaleUntil: order.AfterSaleUntil,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"available": true, "items": items, "total": result.Total,
		"offset": offset, "limit": limit, "truncated": int64(offset+len(items)) < result.Total,
	})
}
