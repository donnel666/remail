package kitesim

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, service *Service, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := &handler{service: service, checker: checker}
	admin := rg.Group("/admin/kitesim")
	admin.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	admin.GET("/phones", middleware.PermissionRequired(checker, "core:resource", "read"), h.listPhones)
	admin.GET("/products", middleware.PermissionRequired(checker, "core:resource", "read"), h.products)
	admin.POST("/accounts/imports", middleware.PermissionRequired(checker, "core:resource", "write"), h.importAccounts)
	admin.POST("/accounts/:accountId/sync", middleware.PermissionRequired(checker, "core:resource", "operate"), h.syncAccount)
	admin.GET("/phones/:phoneId/messages", middleware.PermissionRequired(checker, "mailmatch:message", "read"), h.messages)
	admin.POST(
		"/phones/:phoneId/renewals",
		middleware.PermissionRequired(checker, "core:resource", "operate"),
		middleware.PermissionRequired(checker, "system:settings", "write"),
		h.renewPhone,
	)
	admin.GET("/upstream", middleware.PermissionRequired(checker, "system:settings", "read"), h.upstream)
	admin.PUT("/upstream", middleware.PermissionRequired(checker, "system:settings", "write"), h.putUpstream)
	admin.POST("/upstream/refresh", middleware.PermissionRequired(checker, "system:settings", "write"), h.refreshUpstream)
	admin.POST("/upstream/purchases", middleware.PermissionRequired(checker, "system:settings", "write"), h.purchasePhones)
	admin.POST(
		"/upstream/operations/:operationId/reconcile",
		middleware.PermissionRequired(checker, "system:settings", "write"),
		h.reconcileOperation,
	)
	admin.POST(
		"/upstream/operations/:operationId/resolution",
		middleware.PermissionRequired(checker, "system:settings", "write"),
		middleware.PermissionRequired(checker, "system:settings", "sensitive"),
		h.resolveOperation,
	)
	admin.POST(
		"/upstream/recharges",
		middleware.PermissionRequired(checker, "system:settings", "write"),
		middleware.PermissionRequired(checker, "system:settings", "sensitive"),
		h.rechargeUpstream,
	)
}

type handler struct {
	service *Service
	checker middleware.PermissionChecker
}

func (h *handler) listPhones(c *gin.Context) {
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: 20, MaxLimit: 100})
	if !ok {
		return
	}
	status := AdminPhoneStatus(strings.TrimSpace(c.Query("status")))
	if status != "" && status != AdminPhoneUnsynced {
		if _, valid := providerStatus(status); !valid {
			writeError(c, ErrInvalidInput)
			return
		}
	}
	autoRenew, ok := optionalBool(c, "autoRenew")
	if !ok {
		writeError(c, ErrInvalidInput)
		return
	}
	tokenAvailable, ok := optionalBool(c, "tokenAvailable")
	if !ok {
		writeError(c, ErrInvalidInput)
		return
	}
	syncHealthy, ok := optionalBool(c, "syncHealthy")
	if !ok {
		writeError(c, ErrInvalidInput)
		return
	}
	phoneAvailable, ok := optionalBool(c, "phoneAvailable")
	if !ok {
		writeError(c, ErrInvalidInput)
		return
	}
	createdFrom, ok := optionalTime(c, "createdFrom")
	if !ok {
		writeError(c, ErrInvalidInput)
		return
	}
	createdTo, ok := optionalTime(c, "createdTo")
	if !ok || createdFrom != nil && createdTo != nil && !createdFrom.Before(*createdTo) {
		writeError(c, ErrInvalidInput)
		return
	}
	result, err := h.service.ListPhones(c.Request.Context(), PhoneListFilter{
		Search: c.Query("search"), Status: status,
		AutoRenew: autoRenew, TokenAvailable: tokenAvailable,
		SyncHealthy: syncHealthy, PhoneAvailable: phoneAvailable,
		CreatedFrom: createdFrom, CreatedTo: createdTo,
		Offset: offset, Limit: limit,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

type importRequest struct {
	Content string `json:"content" binding:"required"`
}

func (h *handler) importAccounts(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var request importRequest
	if c.ShouldBindJSON(&request) != nil {
		writeError(c, ErrInvalidInput)
		return
	}
	result, err := h.service.ImportAccounts(c.Request.Context(), request.Content, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, result)
}

func (h *handler) syncAccount(c *gin.Context) {
	accountID, err := pathID(c.Param("accountId"))
	if err != nil {
		writeError(c, ErrAccountMissing)
		return
	}
	task, err := h.service.SyncAccount(c.Request.Context(), accountID, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, task)
}

func (h *handler) messages(c *gin.Context) {
	phoneID, err := pathID(c.Param("phoneId"))
	if err != nil {
		writeError(c, ErrPhoneMissing)
		return
	}
	items, err := h.service.Messages(c.Request.Context(), phoneID, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *handler) products(c *gin.Context) {
	items, err := h.service.ListProducts(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *handler) upstream(c *gin.Context) {
	result, err := h.service.GetUpstream(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

type upstreamUpdateRequest struct {
	AccountID uint         `json:"accountId" binding:"required"`
	Card      *CardProfile `json:"card"`
	ClearCard bool         `json:"clearCard"`
}

func (h *handler) putUpstream(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var request upstreamUpdateRequest
	if c.ShouldBindJSON(&request) != nil || request.Card != nil && request.ClearCard {
		writeError(c, ErrInvalidInput)
		return
	}
	if (request.Card != nil || request.ClearCard) && !h.canWriteSensitive(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
		return
	}
	if err := h.service.SaveUpstream(c.Request.Context(), UpstreamConfigUpdate(request), mutationMeta(c)); err != nil {
		writeError(c, err)
		return
	}
	result, err := h.service.GetUpstream(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h *handler) refreshUpstream(c *gin.Context) {
	result, err := h.service.QueueUpstreamRefresh(c.Request.Context(), mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, result)
}

type purchaseRequest struct {
	ProductID    uint   `json:"productId" binding:"required"`
	Count        int    `json:"count" binding:"required"`
	MaxUnitPrice string `json:"maxUnitPrice" binding:"required"`
}

func (h *handler) purchasePhones(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var request purchaseRequest
	if c.ShouldBindJSON(&request) != nil {
		writeError(c, ErrInvalidInput)
		return
	}
	result, err := h.service.QueuePurchase(c.Request.Context(), request.ProductID, request.Count, request.MaxUnitPrice, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, result)
}

type rechargeRequest struct {
	Amount string `json:"amount" binding:"required"`
	CVC    string `json:"cvc" binding:"required"`
}

func (h *handler) rechargeUpstream(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var request rechargeRequest
	if c.ShouldBindJSON(&request) != nil {
		writeError(c, ErrInvalidInput)
		return
	}
	result, err := h.service.QueueRecharge(c.Request.Context(), request.Amount, request.CVC, mutationMeta(c))
	request.CVC = ""
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, result)
}

func (h *handler) reconcileOperation(c *gin.Context) {
	operationID, err := pathID(c.Param("operationId"))
	if err != nil {
		writeError(c, ErrOperationMissing)
		return
	}
	result, err := h.service.QueueOperationReconcile(c.Request.Context(), uint64(operationID), mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, result)
}

type operationResolutionRequest struct {
	Outcome OperationStatus `json:"outcome" binding:"required"`
	Note    string          `json:"note" binding:"required"`
}

func (h *handler) resolveOperation(c *gin.Context) {
	operationID, err := pathID(c.Param("operationId"))
	if err != nil {
		writeError(c, ErrOperationMissing)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var request operationResolutionRequest
	if c.ShouldBindJSON(&request) != nil {
		writeError(c, ErrInvalidInput)
		return
	}
	result, err := h.service.ResolveOperation(c.Request.Context(), uint64(operationID), request.Outcome, request.Note, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

type renewalRequest struct {
	ProductID    uint   `json:"productId" binding:"required"`
	MaxUnitPrice string `json:"maxUnitPrice" binding:"required"`
}

func (h *handler) renewPhone(c *gin.Context) {
	phoneID, err := pathID(c.Param("phoneId"))
	if err != nil {
		writeError(c, ErrPhoneMissing)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var request renewalRequest
	if c.ShouldBindJSON(&request) != nil {
		writeError(c, ErrInvalidInput)
		return
	}
	result, err := h.service.QueueRenewal(c.Request.Context(), phoneID, request.ProductID, request.MaxUnitPrice, mutationMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, result)
}

func (h *handler) canWriteSensitive(c *gin.Context) bool {
	userID, userOK := middleware.GetCurrentUserID(c)
	role, roleOK := middleware.GetCurrentRole(c)
	if !userOK || !roleOK || h.checker == nil {
		return false
	}
	allowed, err := h.checker.Check(c.Request.Context(), userID, role, "system:settings", "sensitive")
	return err == nil && allowed
}

func mutationMeta(c *gin.Context) MutationMeta {
	operatorID, _ := middleware.GetCurrentUserID(c)
	path := ""
	if c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	return MutationMeta{
		OperatorUserID: operatorID, IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID: middleware.GetRequestID(c), Path: path,
	}
}

func pathID(raw string) (uint, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, ErrInvalidInput
	}
	return uint(value), nil
}

func optionalBool(c *gin.Context, name string) (*bool, bool) {
	raw, exists := c.GetQuery(name)
	if !exists || strings.TrimSpace(raw) == "" {
		return nil, true
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return nil, false
	}
	return &value, true
}

func optionalTime(c *gin.Context, name string) (*time.Time, bool) {
	raw, exists := c.GetQuery(name)
	if !exists || strings.TrimSpace(raw) == "" {
		return nil, true
	}
	value, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return nil, false
	}
	value = value.UTC()
	return &value, true
}

func writeError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, ErrInvalidInput):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid Kitesim request.", "requestId": requestID})
	case errors.Is(err, ErrAccountMissing), errors.Is(err, ErrPhoneMissing), errors.Is(err, ErrOperationMissing):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	case errors.Is(err, ErrUpstreamNotConfigured):
		c.JSON(http.StatusConflict, gin.H{"message": "Kitesim system account is not configured.", "requestId": requestID})
	case errors.Is(err, ErrCardNotConfigured):
		c.JSON(http.StatusConflict, gin.H{"message": "Kitesim credit card is not configured.", "requestId": requestID})
	case errors.Is(err, ErrOperationBusy):
		c.JSON(http.StatusConflict, gin.H{"message": "Another Kitesim money operation is still active.", "requestId": requestID})
	case errors.Is(err, ErrIdempotencyRequired):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Idempotency-Key is required.", "requestId": requestID})
	case errors.Is(err, ErrIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency-Key conflicts with a different request.", "requestId": requestID})
	case errors.Is(err, ErrPriceChanged):
		c.JSON(http.StatusConflict, gin.H{"message": "Kitesim price changed. Refresh products and confirm the new price.", "requestId": requestID})
	case errors.Is(err, ErrOperationState):
		c.JSON(http.StatusConflict, gin.H{"message": "Kitesim operation is not allowed in its current state.", "requestId": requestID})
	case errors.Is(err, ErrLoginFailed):
		c.JSON(http.StatusBadGateway, gin.H{"message": "Kitesim login failed. Check the platform account and password.", "requestId": requestID})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"message": "Kitesim is temporarily unavailable.", "requestId": requestID})
	}
}
