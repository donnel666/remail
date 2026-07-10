package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/mailmatch/domain"
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
	if !globalPickupListLimiter.allow(tokenPlain) {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "Too many requests.", "requestId": middleware.GetRequestID(c)})
		return
	}
	items, state, err := h.mod.UseCase.ListPickupMail(c.Request.Context(), tokenPlain, email)
	if err != nil {
		writeMailmatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderMailResponse(items, state))
}

func (h *Handler) GetPickupMessage(c *gin.Context) {
	email, tokenPlain, ok := pickupCredential(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	messageID, err := strconv.ParseUint(strings.TrimSpace(c.Param("messageId")), 10, 64)
	if err != nil || messageID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": middleware.GetRequestID(c)})
		return
	}
	if !globalPickupDetailLimiter.allow(tokenPlain) {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "Too many requests.", "requestId": middleware.GetRequestID(c)})
		return
	}
	item, err := h.mod.UseCase.GetPickupMessage(c.Request.Context(), tokenPlain, email, uint(messageID))
	if err != nil {
		writeMailmatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, MailContentDetailResponse{
		MailContentResponse: mailContentResponse(*item),
		Body:                item.Body,
	})
}

const maxPickupLimiterKeys = 100000

var (
	globalPickupListLimiter   = newPickupLimiter(rate.Limit(1), 3)
	globalPickupDetailLimiter = newPickupLimiter(rate.Limit(10), 20)
)

type pickupLimiter struct {
	mu    sync.Mutex
	items map[string]*pickupLimiterEntry
	limit rate.Limit
	burst int
}

type pickupLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newPickupLimiter(limit rate.Limit, burst int) *pickupLimiter {
	return &pickupLimiter{
		items: make(map[string]*pickupLimiterEntry),
		limit: limit,
		burst: burst,
	}
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
		entry = &pickupLimiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
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
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func pickupCredential(c *gin.Context) (email string, token string, ok bool) {
	email = strings.ToLower(strings.TrimSpace(c.Query("email")))
	token = strings.TrimSpace(c.Query("token"))
	return email, token, email != "" && token != ""
}

func orderMailResponse(items []domain.MailContent, state *domain.FetchState) OrderMailResponse {
	resp := OrderMailResponse{Items: make([]MailContentResponse, len(items))}
	for i := range items {
		resp.Items[i] = mailContentResponse(items[i])
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

func mailContentResponse(item domain.MailContent) MailContentResponse {
	return MailContentResponse{
		ID:               item.ID,
		Sender:           item.Sender,
		Recipient:        item.Recipient,
		ReceivedAt:       item.ReceivedAt,
		Subject:          item.Subject,
		BodyPreview:      item.BodyPreview,
		VerificationCode: item.VerificationCode,
	}
}

func writeMailmatchError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, domain.ErrPickupCredentialInvalid):
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Credential is invalid or expired.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Order not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderForbidden):
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": requestID})
	case errors.Is(err, domain.ErrOrderUnavailable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Order is not available for mail reading.", "requestId": requestID})
	case errors.Is(err, domain.ErrMessageNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Message not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrFetchQueueUnavailable), errors.Is(err, domain.ErrMailServiceUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
