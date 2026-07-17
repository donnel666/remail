package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	"github.com/donnel666/remail/internal/platform"
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
		platform.RecordBusinessEvent("checkout", checkoutMetricResult(err))
		writeTradeError(c, err)
		return
	}
	platform.RecordBusinessEvent("checkout", "succeeded")
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, orderResponse(*result))
}

func checkoutMetricResult(err error) string {
	switch {
	case errors.Is(err, domain.ErrInsufficientBalance):
		return "insufficient_balance"
	case errors.Is(err, domain.ErrInsufficientInventory):
		return "insufficient_inventory"
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return "idempotency_conflict"
	default:
		return "failed"
	}
}

func (h *Handler) GetOrders(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	offset, limit, ok := parseOrderListOffsetLimit(c)
	if !ok {
		return
	}
	var afterID uint
	if raw := strings.TrimSpace(c.Query("afterId")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
			return
		}
		afterID = uint(parsed)
	}
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
	domainFilter, ok := parseOrderDomain(c.Query("domain"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	createdFrom, ok := parseOptionalTime(c.Query("createdFrom"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	createdTo, ok := parseOptionalTime(c.Query("createdTo"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	role, _ := middleware.GetCurrentRole(c)
	isAdmin := role.HasAdminAccess()
	result, err := h.mod.UseCase.ListOrders(c.Request.Context(), tradeapp.OrderListFilter{
		UserID:      userID,
		IsAdmin:     isAdmin,
		Scope:       strings.TrimSpace(c.DefaultQuery("scope", "mine")),
		Status:      status,
		ServiceMode: serviceMode,
		Search:      strings.TrimSpace(c.Query("search")),
		Domain:      domainFilter,
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	}, offset, afterID, limit)
	if err != nil {
		writeTradeError(c, err)
		return
	}
	resp := OrderListResponse{
		Items:       make([]OrderResponse, len(result.Items)),
		Total:       result.Total,
		Offset:      offset,
		NextAfterID: result.NextAfterID,
		HasNext:     result.NextAfterID != nil,
		Limit:       limit,
		Facets:      toOrderListFacetsResponse(result.Facets),
	}
	for i := range result.Items {
		resp.Items[i] = orderResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GetOrder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	role, _ := middleware.GetCurrentRole(c)
	result, err := h.mod.UseCase.GetOrder(c.Request.Context(), c.Param("orderNo"), userID, role.HasAdminAccess())
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
	offset, limit, ok := parseOffsetLimit(c)
	if !ok {
		return
	}
	role, _ := middleware.GetCurrentRole(c)
	items, total, err := h.mod.UseCase.ListEvents(c.Request.Context(), c.Param("orderNo"), userID, role.HasAdminAccess(), offset, limit)
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
	h.respondOrder(c, http.StatusOK, order.OrderNo, userID, false)
}

func (h *Handler) PostAdminOrderRefund(c *gin.Context) {
	operatorUserID, ok := currentUserID(c)
	if !ok {
		return
	}
	var req AdminOrderCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.refund", c.Param("orderNo"), "failure", "Order refund failed.")
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	order, err := h.mod.UseCase.AdminRefundOrder(c.Request.Context(), tradeapp.AdminOrderCommandRequest{
		OrderNo:        c.Param("orderNo"),
		Reason:         req.Reason,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
		OperatorUserID: operatorUserID,
	})
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.refund", c.Param("orderNo"), "failure", "Order refund failed.")
		writeTradeError(c, err)
		return
	}
	_ = h.writeOperationLog(c, operatorUserID, "trade.order.refund", c.Param("orderNo"), "success", "Order refunded.")
	h.respondOrder(c, http.StatusOK, order.OrderNo, operatorUserID, true)
}

func (h *Handler) PostAdminOrderTerminate(c *gin.Context) {
	operatorUserID, ok := currentUserID(c)
	if !ok {
		return
	}
	var req AdminOrderCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.terminate", c.Param("orderNo"), "failure", "Order termination failed.")
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	order, err := h.mod.UseCase.AdminTerminateOrder(c.Request.Context(), tradeapp.AdminOrderCommandRequest{
		OrderNo:        c.Param("orderNo"),
		Reason:         req.Reason,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
		OperatorUserID: operatorUserID,
	})
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.terminate", c.Param("orderNo"), "failure", "Order termination failed.")
		writeTradeError(c, err)
		return
	}
	_ = h.writeOperationLog(c, operatorUserID, "trade.order.terminate", c.Param("orderNo"), "success", "Order terminated.")
	h.respondOrder(c, http.StatusOK, order.OrderNo, operatorUserID, true)
}

func (h *Handler) PostAdminOrderCleanupRetry(c *gin.Context) {
	operatorUserID, ok := currentUserID(c)
	if !ok {
		return
	}
	order, err := h.mod.UseCase.AdminRetryOrderCleanup(c.Request.Context(), c.Param("orderNo"), middleware.GetRequestID(c))
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.cleanup_retry", c.Param("orderNo"), "failure", "Order cleanup retry failed.")
		writeTradeError(c, err)
		return
	}
	_ = h.writeOperationLog(c, operatorUserID, "trade.order.cleanup_retry", c.Param("orderNo"), "success", "Order cleanup retried.")
	h.respondOrder(c, http.StatusOK, order.OrderNo, operatorUserID, true)
}

func (h *Handler) PostAdminOrderRefundRetry(c *gin.Context) {
	operatorUserID, ok := currentUserID(c)
	if !ok {
		return
	}
	var req AdminOrderCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.refund_retry", c.Param("orderNo"), "failure", "Order refund retry failed.")
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	order, err := h.mod.UseCase.AdminRetryOrderRefund(c.Request.Context(), tradeapp.AdminOrderCommandRequest{
		OrderNo:        c.Param("orderNo"),
		Reason:         req.Reason,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID:      middleware.GetRequestID(c),
		OperatorUserID: operatorUserID,
	})
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.refund_retry", c.Param("orderNo"), "failure", "Order refund retry failed.")
		writeTradeError(c, err)
		return
	}
	_ = h.writeOperationLog(c, operatorUserID, "trade.order.refund_retry", c.Param("orderNo"), "success", "Order refund retried.")
	h.respondOrder(c, http.StatusOK, order.OrderNo, operatorUserID, true)
}

func (h *Handler) respondOrder(c *gin.Context, status int, orderNo string, userID uint, isAdmin bool) {
	result, err := h.mod.UseCase.GetOrder(c.Request.Context(), orderNo, userID, isAdmin)
	if err != nil {
		writeTradeError(c, err)
		return
	}
	c.JSON(status, orderResponse(*result))
}

func (h *Handler) PostAdminOrderTimeoutScan(c *gin.Context) {
	operatorUserID, ok := currentUserID(c)
	if !ok {
		return
	}
	result, err := h.mod.UseCase.ExpireDueOrders(c.Request.Context(), 0)
	if err != nil {
		_ = h.writeOperationLog(c, operatorUserID, "trade.order.timeout_scan", "timeouts", "failure", "Order timeout scan failed.")
		writeTradeError(c, err)
		return
	}
	_ = h.writeOperationLog(c, operatorUserID, "trade.order.timeout_scan", "timeouts", "success", "Order timeout scan completed.")
	c.JSON(http.StatusAccepted, expireOrdersResponse(result))
}

func (h *Handler) writeOperationLog(c *gin.Context, operatorUserID uint, operationType, resourceID, result, summary string) error {
	log := h.operationLog(c, operatorUserID, operationType, resourceID, result, summary)
	if log == nil {
		return nil
	}
	return h.mod.OperationLogs.Create(c.Request.Context(), log)
}

func expireOrdersResponse(result *tradeapp.ExpireOrdersResult) ExpireOrdersResponse {
	if result == nil {
		return ExpireOrdersResponse{}
	}
	return ExpireOrdersResponse{
		CodeTimedOut:                result.CodeTimedOut,
		PurchaseActivationCompleted: result.PurchaseActivationCompleted,
		PurchaseWarrantyCompleted:   result.PurchaseWarrantyCompleted,
		CodeCleaned:                 result.CodeCleaned,
		CleanupRetried:              result.CleanupRetried,
		DeliveryReconciled:          result.DeliveryReconciled,
		Failed:                      result.Failed,
	}
}

func (h *Handler) operationLog(c *gin.Context, operatorUserID uint, operationType, resourceID, result, summary string) *governancedomain.OperationLog {
	if h.mod == nil || h.mod.OperationLogs == nil {
		return nil
	}
	return &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  operationType,
		ResourceType:   "order",
		ResourceID:     strings.TrimSpace(resourceID),
		Path:           c.FullPath(),
		Result:         result,
		SafeSummary:    summary,
		RequestID:      middleware.GetRequestID(c),
	}
}

func currentUserID(c *gin.Context) (uint, bool) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return userID, true
}

func parseOffsetLimit(c *gin.Context) (int, int, bool) {
	return middleware.ParsePagination(c, middleware.PaginationOptions{
		DefaultLimit: 20,
		MaxLimit:     100,
	})
}

// parseOrderListOffsetLimit allows larger pages than parseOffsetLimit because
// the console order list loads 1000-row blocks.
func parseOrderListOffsetLimit(c *gin.Context) (int, int, bool) {
	return middleware.ParsePagination(c, middleware.PaginationOptions{
		DefaultLimit: 20,
		MaxLimit:     1000,
	})
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

var orderDomainPattern = regexp.MustCompile(`^[A-Za-z0-9.-]{1,255}$`)

func parseOrderDomain(raw string) (string, bool) {
	normalized := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "@")
	if normalized == "" {
		return "", true
	}
	if !orderDomainPattern.MatchString(normalized) {
		return "", false
	}
	return normalized, true
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

func toOrderListFacetsResponse(facets *tradeapp.OrderListFacets) *OrderListFacetsResponse {
	if facets == nil {
		return nil
	}
	domains := make([]OrderKeyFacetResponse, len(facets.Domains))
	for i := range facets.Domains {
		domains[i] = OrderKeyFacetResponse{
			Key:   facets.Domains[i].Key,
			Count: facets.Domains[i].Count,
		}
	}
	return &OrderListFacetsResponse{
		Status: OrderStatusFacetsResponse{
			All:            facets.Status.All,
			PendingPayment: facets.Status.PendingPayment,
			Paid:           facets.Status.Paid,
			Active:         facets.Status.Active,
			Completed:      facets.Status.Completed,
			Refunded:       facets.Status.Refunded,
			Failed:         facets.Status.Failed,
			Closed:         facets.Status.Closed,
		},
		ServiceMode: OrderServiceModeFacetsResponse{
			All:      facets.ServiceMode.All,
			Code:     facets.ServiceMode.Code,
			Purchase: facets.ServiceMode.Purchase,
		},
		Domains: domains,
	}
}

func orderResponse(result tradeapp.CheckoutResult) OrderResponse {
	order := result.Order
	allocationType := ""
	allocationID := uint(0)
	var projectLogoURL *string
	if value := strings.TrimSpace(result.ProjectLogoURL); value != "" {
		projectLogoURL = &value
	}
	var owner *OrderOwnerResponse
	if result.Owner != nil {
		owner = &OrderOwnerResponse{
			UserID:    result.Owner.ID,
			Email:     result.Owner.Email,
			Nickname:  result.Owner.Nickname,
			GroupName: result.Owner.GroupName,
			Role:      result.Owner.Role,
			Enabled:   result.Owner.Enabled,
		}
	}
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
		Owner:                owner,
		ProjectID:            order.ProjectID,
		ProjectName:          result.ProjectName,
		ProjectLogoURL:       projectLogoURL,
		ProjectProductID:     order.ProjectProductID,
		ProductType:          string(order.ProductType),
		ServiceMode:          string(order.ServiceMode),
		SupplyPolicy:         string(order.SupplyPolicy),
		Status:               string(order.Status),
		FailureCode:          string(order.FailureCode),
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
		HasDelivery:          result.HasDelivery,
		VerificationCode:     result.VerificationCode,
		LastMailReceivedAt:   result.LastMailReceivedAt,
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
