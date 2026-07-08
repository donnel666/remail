package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	mod *Module
}

func NewHandler(mod *Module) *Handler {
	return &Handler{mod: mod}
}

func (h *Handler) PostOrder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	channel, _ := openapiapi.CurrentClientChannel(c)
	if channel == "" {
		channel = openapiapi.ClientChannelConsole
	}
	var apiKeyID *uint
	if id, ok := openapiapi.CurrentAPIKeyID(c); ok && id > 0 {
		apiKeyID = &id
	}
	result, err := h.mod.UseCase.Checkout(c.Request.Context(), tradeapp.CheckoutRequest{
		UserID:         userID,
		ProjectID:      req.ProjectID,
		ProductID:      req.ProductID,
		ServiceMode:    c.DefaultQuery("serviceMode", string(domain.ServiceModePurchase)),
		SupplyPolicy:   c.DefaultQuery("supply", string(domain.SupplyPolicyPrivateFirst)),
		EmailSuffix:    req.EmailSuffix,
		ClientChannel:  domain.ClientChannel(channel),
		APIKeyID:       apiKeyID,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
	})
	if err != nil {
		writeTradeError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, orderResponse(*result))
}

func (h *Handler) GetOrders(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	offset, limit := parseOffsetLimit(c)
	status, ok := parseOrderStatus(c.Query("status"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	serviceMode, ok := parseServiceMode(c.Query("serviceMode"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	role, _ := middleware.GetCurrentRoleLevel(c)
	isAdmin := role.IsAtLeast(iamdomain.RoleAdmin)
	items, total, err := h.mod.UseCase.ListOrders(c.Request.Context(), tradeapp.OrderListFilter{
		UserID:      userID,
		IsAdmin:     isAdmin,
		Scope:       strings.TrimSpace(c.DefaultQuery("scope", "mine")),
		Status:      status,
		ServiceMode: serviceMode,
		Search:      strings.TrimSpace(c.Query("search")),
	}, offset, limit)
	if err != nil {
		writeTradeError(c, err)
		return
	}
	resp := OrderListResponse{Items: make([]OrderResponse, len(items)), Total: total, Offset: offset, Limit: limit}
	for i := range items {
		resp.Items[i] = orderResponse(items[i])
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GetOrder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	role, _ := middleware.GetCurrentRoleLevel(c)
	result, err := h.mod.UseCase.GetOrder(c.Request.Context(), c.Param("orderNo"), userID, role.IsAtLeast(iamdomain.RoleAdmin))
	if err != nil {
		writeTradeError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderResponse(*result))
}

func (h *Handler) GetOrderEvents(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	offset, limit := parseOffsetLimit(c)
	role, _ := middleware.GetCurrentRoleLevel(c)
	items, total, err := h.mod.UseCase.ListEvents(c.Request.Context(), c.Param("orderNo"), userID, role.IsAtLeast(iamdomain.RoleAdmin), offset, limit)
	if err != nil {
		writeTradeError(c, err)
		return
	}
	resp := OrderEventListResponse{Items: make([]OrderEventResponse, len(items)), Total: total, Offset: offset, Limit: limit}
	for i := range items {
		resp.Items[i] = orderEventResponse(items[i])
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) PostOrderArchive(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	order, err := h.mod.UseCase.Archive(c.Request.Context(), c.Param("orderNo"), userID)
	if err != nil {
		writeTradeError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderResponse(tradeapp.CheckoutResult{Order: *order}))
}

func currentUserID(c *gin.Context) (uint, bool) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return userID, true
}

func parseOffsetLimit(c *gin.Context) (int, int) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return offset, limit
}

func parseOrderStatus(raw string) (domain.OrderStatus, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	switch domain.OrderStatus(raw) {
	case domain.OrderStatusPendingPayment, domain.OrderStatusPaid, domain.OrderStatusActive, domain.OrderStatusCompleted, domain.OrderStatusRefunded, domain.OrderStatusFailed, domain.OrderStatusClosed:
		return domain.OrderStatus(raw), true
	default:
		return "", false
	}
}

func parseServiceMode(raw string) (domain.ServiceMode, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	return domain.NormalizeServiceMode(raw)
}

func orderResponse(result tradeapp.CheckoutResult) OrderResponse {
	order := result.Order
	allocationType := ""
	allocationID := uint(0)
	if order.AllocationType != nil {
		allocationType = string(*order.AllocationType)
	}
	switch {
	case order.MicrosoftAllocID != nil:
		allocationID = *order.MicrosoftAllocID
	case order.DomainAllocID != nil:
		allocationID = *order.DomainAllocID
	}
	return OrderResponse{
		ID:                   order.ID,
		OrderNo:              order.OrderNo,
		UserID:               order.UserID,
		ProjectID:            order.ProjectID,
		ProjectProductID:     order.ProjectProductID,
		ProductType:          string(order.ProductType),
		ServiceMode:          string(order.ServiceMode),
		SupplyPolicy:         string(order.SupplyPolicy),
		Status:               string(order.Status),
		PayAmount:            order.PayAmount,
		RefundAmount:         order.RefundAmount,
		AllocationType:       allocationType,
		AllocationID:         allocationID,
		DeliveryEmail:        order.DeliveryEmail,
		ReceiveStartedAt:     order.ReceiveStartedAt,
		ReceiveUntil:         order.ReceiveUntil,
		ActivatedAt:          order.ActivatedAt,
		AfterSaleUntil:       order.AfterSaleUntil,
		ClientChannel:        string(order.ClientChannel),
		APIKeyID:             order.APIKeyID,
		ServiceCleanupStatus: order.ServiceCleanupStatus,
		ServiceToken:         result.ServiceToken,
		ArchivedAt:           order.ArchivedAt,
		CreatedAt:            order.CreatedAt,
		UpdatedAt:            order.UpdatedAt,
	}
}

func orderEventResponse(item domain.OrderEvent) OrderEventResponse {
	resp := OrderEventResponse{
		EventNo:      item.EventNo,
		OrderNo:      item.OrderNo,
		EventType:    item.EventType,
		OperatorType: string(item.OperatorType),
		Reason:       item.Reason,
		CreatedAt:    item.CreatedAt,
	}
	if item.FromStatus != nil {
		resp.FromStatus = string(*item.FromStatus)
	}
	if item.ToStatus != nil {
		resp.ToStatus = string(*item.ToStatus)
	}
	return resp
}

func writeTradeError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrIdempotencyRequired):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Idempotency-Key is required.", "requestId": requestID})
	case errors.Is(err, domain.ErrIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency-Key conflicts with a different request.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Order not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderForbidden):
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": requestID})
	case errors.Is(err, domain.ErrInsufficientBalance):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Insufficient balance.", "requestId": requestID})
	case errors.Is(err, domain.ErrInsufficientInventory):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Insufficient inventory.", "requestId": requestID})
	case errors.Is(err, domain.ErrProjectUnavailable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Project is not available for ordering.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidOrderRequest), errors.Is(err, domain.ErrOrderStateConflict):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid order request.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
