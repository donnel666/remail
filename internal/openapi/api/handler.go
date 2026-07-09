package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/donnel666/remail/api/middleware"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	"github.com/donnel666/remail/internal/openapi/domain"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	mod *Module
}

func NewHandler(mod *Module) *Handler {
	return &Handler{mod: mod}
}

func (h *Handler) PostAPIKey(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	var req KeyCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	item, err := h.mod.UseCase.CreateAPIKey(c.Request.Context(), openapiapp.CreateAPIKeyRequest{
		UserID:             userID,
		Name:               req.Name,
		ExpireAt:           req.ExpireAt,
		RateLimitPerMinute: req.RateLimitPerMinute,
		ConcurrencyLimit:   req.ConcurrencyLimit,
		QuotaLimit:         req.QuotaLimit,
		IdempotencyKey:     c.GetHeader("Idempotency-Key"),
		RequestID:          middleware.GetRequestID(c),
	})
	if err != nil {
		writeOpenAPIError(c, err)
		return
	}
	c.JSON(http.StatusCreated, apiKeyResponse(*item, true))
}

func (h *Handler) GetAPIKeys(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	offset, limit := parseOffsetLimit(c)
	items, total, err := h.mod.UseCase.ListAPIKeys(c.Request.Context(), userID, offset, limit)
	if err != nil {
		writeOpenAPIError(c, err)
		return
	}
	resp := KeyListResponse{Items: make([]KeyResponse, len(items)), Total: total, Offset: offset, Limit: limit}
	for i := range items {
		resp.Items[i] = apiKeyResponse(items[i], true)
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GetAPIKeyUsage(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	usage, err := h.mod.UseCase.GetAPIKeyUsage(c.Request.Context(), userID)
	if err != nil {
		writeOpenAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, KeyUsageResponse{
		RequestCount: usage.RequestCount,
		KeyCount:     usage.KeyCount,
	})
}

func (h *Handler) GetAPIKeyProfile(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	keyID, ok := CurrentAPIKeyID(c)
	if !ok || keyID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return
	}
	item, err := h.mod.UseCase.GetAPIKey(c.Request.Context(), userID, keyID)
	if err != nil {
		writeOpenAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, KeyProfileResponse{APIKey: apiKeyResponse(*item, false)})
}

func (h *Handler) GetAPIKey(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	keyID, ok := parseUintParam(c, "keyId")
	if !ok {
		return
	}
	item, err := h.mod.UseCase.GetAPIKey(c.Request.Context(), userID, keyID)
	if err != nil {
		writeOpenAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, apiKeyResponse(*item, true))
}

func (h *Handler) DeleteAPIKey(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	keyID, ok := parseUintParam(c, "keyId")
	if !ok {
		return
	}
	if err := h.mod.UseCase.DeleteAPIKey(c.Request.Context(), userID, keyID); err != nil {
		writeOpenAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) PatchAPIKey(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	keyID, ok := parseUintParam(c, "keyId")
	if !ok {
		return
	}
	var req KeyPatchRequest
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "requestId": middleware.GetRequestID(c)})
		return
	}
	if _, exists := raw["expireAt"]; exists {
		req.ExpireSet = true
	}
	if _, exists := raw["rateLimitPerMinute"]; exists {
		req.RateLimitSet = true
	}
	if _, exists := raw["quotaLimit"]; exists {
		req.QuotaSet = true
	}
	item, err := h.mod.UseCase.UpdateAPIKey(c.Request.Context(), openapiapp.UpdateAPIKeyRequest{
		UserID:             userID,
		KeyID:              keyID,
		Name:               req.Name,
		Enabled:            req.Enabled,
		ExpireAt:           req.ExpireAt,
		ExpireSet:          req.ExpireSet || req.ExpireAt != nil,
		RateLimitPerMinute: req.RateLimitPerMinute,
		RateLimitSet:       req.RateLimitSet,
		ConcurrencyLimit:   req.ConcurrencyLimit,
		QuotaLimit:         req.QuotaLimit,
		QuotaSet:           req.QuotaSet,
	})
	if err != nil {
		writeOpenAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, apiKeyResponse(*item, true))
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

func parseUintParam(c *gin.Context, name string) (uint, bool) {
	raw := c.Param(name)
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return 0, false
	}
	return uint(value), true
}

func apiKeyResponse(item domain.APIKey, includePlain bool) KeyResponse {
	resp := KeyResponse{
		ID:                 item.ID,
		Name:               item.Name,
		KeyPrefix:          item.KeyPrefix,
		Enabled:            item.Enabled,
		RateLimitPerMinute: item.RateLimitPerMinute,
		ConcurrencyLimit:   item.ConcurrencyLimit,
		QuotaLimit:         item.QuotaLimit,
		QuotaUsed:          item.QuotaUsed,
		RemainingQuota:     remainingAPIKeyQuota(item),
		ActiveRequests:     item.ActiveRequests,
		ExpireAt:           item.ExpireAt,
		LastUsedAt:         item.LastUsedAt,
		CreatedAt:          item.CreatedAt,
		UpdatedAt:          item.UpdatedAt,
	}
	if includePlain {
		resp.KeyPlain = item.KeyPlain
	}
	return resp
}

func writeOpenAPIError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrIdempotencyRequired):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Idempotency-Key is required.", "requestId": requestID})
	case errors.Is(err, domain.ErrIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency-Key conflicts with a different request.", "requestId": requestID})
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "API key not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidAPIKey), errors.Is(err, domain.ErrInvalidCredentialFilter):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid API key request.", "requestId": requestID})
	case errors.Is(err, domain.ErrAPIKeyRateLimited):
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "Too many requests.", "requestId": requestID})
	case errors.Is(err, domain.ErrAPIKeyQuotaExceeded):
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "API key quota exceeded.", "requestId": requestID})
	case errors.Is(err, domain.ErrAPIKeyConcurrencyLimit):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Credential concurrency limit reached.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}

func remainingAPIKeyQuota(item domain.APIKey) *int64 {
	if item.QuotaLimit == nil {
		return nil
	}
	remaining := *item.QuotaLimit - item.QuotaUsed
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}
