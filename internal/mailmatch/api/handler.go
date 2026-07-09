package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapidomain "github.com/donnel666/remail/internal/openapi/domain"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type Handler struct {
	mod *Module
}

func NewHandler(mod *Module) *Handler {
	return &Handler{mod: mod}
}

func (h *Handler) GetPickupMessages(c *gin.Context) {
	email, tokenPlain, ok := pickupCredential(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	if !globalPickupLimiter.allow(tokenPlain) {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "Too many requests.", "requestId": middleware.GetRequestID(c)})
		return
	}
	startedAt := time.Now()
	token, err := h.mod.OpenAPI.FindOrderTokenByPlain(c.Request.Context(), tokenPlain)
	if err != nil {
		writeOrderTokenError(c, err)
		return
	}
	defer h.logOrderTokenRequest(c, token.ID, startedAt)
	items, state, err := h.mod.UseCase.ListOrderMailByServiceToken(c.Request.Context(), token.OrderNo, email)
	if err != nil {
		writeMailmatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderMailResponse(items, state))
}

const maxPickupLimiterKeys = 100000

var globalPickupLimiter = newPickupLimiter()

type pickupLimiter struct {
	mu    sync.Mutex
	items map[string]*pickupLimiterEntry
}

type pickupLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newPickupLimiter() *pickupLimiter {
	return &pickupLimiter{items: make(map[string]*pickupLimiterEntry)}
}

func (l *pickupLimiter) allow(token string) bool {
	key := pickupLimitKey(token)
	if key == "" {
		return false
	}
	now := time.Now()
	l.mu.Lock()
	entry := l.items[key]
	if entry == nil {
		entry = &pickupLimiterEntry{limiter: rate.NewLimiter(rate.Limit(1), 3)}
		l.items[key] = entry
	}
	entry.lastSeen = now
	if len(l.items) > maxPickupLimiterKeys {
		l.pruneLocked(now)
	}
	limiter := entry.limiter
	l.mu.Unlock()
	return limiter.Allow()
}

func (l *pickupLimiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-10 * time.Minute)
	for key, entry := range l.items {
		if entry.lastSeen.Before(cutoff) {
			delete(l.items, key)
		}
	}
	if len(l.items) <= maxPickupLimiterKeys {
		return
	}
	for key := range l.items {
		delete(l.items, key)
		if len(l.items) <= maxPickupLimiterKeys {
			return
		}
	}
}

func pickupLimitKey(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 14 {
		return token
	}
	return token[:14]
}

func pickupCredential(c *gin.Context) (email string, token string, ok bool) {
	email = strings.ToLower(strings.TrimSpace(c.Query("email")))
	token = strings.TrimSpace(c.Query("token"))
	return email, token, email != "" && token != ""
}

func (h *Handler) logOrderTokenRequest(c *gin.Context, tokenID uint, startedAt time.Time) {
	if h == nil || h.mod == nil || h.mod.OpenAPI == nil || tokenID == 0 {
		return
	}
	_ = h.mod.OpenAPI.LogAPIRequest(c.Request.Context(), openapiapp.LogAPIRequestRequest{
		PrincipalType: "order_token",
		PrincipalID:   tokenID,
		Path:          c.FullPath(),
		Method:        c.Request.Method,
		HTTPStatus:    c.Writer.Status(),
		DurationMs:    int(time.Since(startedAt) / time.Millisecond),
		RequestID:     middleware.GetRequestID(c),
	})
}

func orderMailResponse(items []domain.MailContent, state *domain.FetchState) OrderMailResponse {
	resp := OrderMailResponse{Items: make([]MailContentResponse, len(items))}
	for i := range items {
		resp.Items[i] = MailContentResponse{
			Sender:           items[i].Sender,
			Recipient:        items[i].Recipient,
			ReceivedAt:       items[i].ReceivedAt,
			Subject:          items[i].Subject,
			Body:             items[i].Body,
			VerificationCode: items[i].VerificationCode,
		}
	}
	if state != nil {
		resp.Fetch = &FetchStateResponse{
			LastJobID:          state.LastJobID,
			LastStatus:         state.LastStatus,
			LastSubmittedAt:    state.LastSubmittedAt,
			LastSuccessAt:      state.LastSuccessAt,
			LastReceivedAt:     state.LastReceivedAt,
			NextFetchAllowedAt: state.CooldownUntil,
			LastSafeError:      state.LastSafeError,
		}
	}
	return resp
}

func writeOrderTokenError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, openapidomain.ErrInvalidOrderToken), errors.Is(err, openapidomain.ErrOrderTokenDisabled), errors.Is(err, openapidomain.ErrOrderTokenExpired):
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Credential is invalid or expired.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}

func writeMailmatchError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Order not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderForbidden):
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderUnavailable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Order is not available for mail reading.", "requestId": requestID})
	case errors.Is(err, domain.ErrFetchQueueUnavailable), errors.Is(err, domain.ErrMailServiceUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
