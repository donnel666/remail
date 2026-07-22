package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	stdmail "net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), pickupTimeout)
	defer cancel()
	queuedAt := time.Now()
	release, admitted := acquirePickup(ctx)
	if !admitted {
		writePickupUnavailable(c)
		return
	}
	defer release()
	platform.ObserveQueueWait("pickup_single", queuedAt)
	serviceStarted := time.Now()
	defer platform.ObserveTaskService("pickup_single", serviceStarted)
	items, state, err := h.mod.UseCase.ListPickupMail(ctx, tokenPlain, email)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writePickupUnavailable(c)
			return
		}
		writeMailmatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderMailResponse(items, state))
}

func (h *Handler) PostPickupMessagesBatch(c *gin.Context) {
	if !globalPickupBatchIPLimiter.allow(normalizePickupClientIP(c.ClientIP())) {
		c.Header("Retry-After", "10")
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "Too many requests.", "requestId": middleware.GetRequestID(c)})
		return
	}
	var req PickupBatchRequest
	if err := bindPickupBatchJSON(c, &req); err != nil {
		writePickupBatchBodyError(c, err)
		return
	}
	if len(req.Items) < 2 || len(req.Items) > maxPickupBatchSize {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Pickup batch must contain between 2 and 200 items.", "requestId": middleware.GetRequestID(c)})
		return
	}
	resp := make(PickupBatchResponse, len(req.Items))
	credentials := make([]mailmatchapp.PickupCredential, 0, len(req.Items))
	credentialIndexes := make([]int, 0, len(req.Items))
	failed := false
	for i := range req.Items {
		resp[i].Index = i
		credential := mailmatchapp.PickupCredential{
			Email: strings.ToLower(strings.TrimSpace(req.Items[i].Email)),
			Token: strings.TrimSpace(req.Items[i].Token),
		}
		if !validPickupCredential(credential) {
			resp[i].Status = "failed"
			resp[i].Error = &PickupBatchItemErrorResponse{Code: "invalid_request", Message: "Invalid pickup credential."}
			failed = true
			continue
		}
		if !globalPickupListLimiter.allow(credential.Token) {
			c.Header("Retry-After", "1")
			resp[i].Status = "failed"
			resp[i].Error = &PickupBatchItemErrorResponse{Code: "rate_limited", Message: "Too many requests."}
			failed = true
			continue
		}
		credentials = append(credentials, credential)
		credentialIndexes = append(credentialIndexes, i)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), pickupTimeout)
	defer cancel()
	queuedAt := time.Now()
	release, admitted := acquirePickup(ctx)
	if !admitted {
		writePickupUnavailable(c)
		return
	}
	defer release()
	platform.ObserveQueueWait("pickup_batch", queuedAt)
	serviceStarted := time.Now()
	defer platform.ObserveTaskService("pickup_batch", serviceStarted)
	results := h.mod.UseCase.ListPickupMailBatch(ctx, credentials)
	for i := range results {
		index := credentialIndexes[i]
		if results[i].Err != nil {
			resp[index].Status = "failed"
			resp[index].Error = pickupBatchItemError(results[i].Err)
			if resp[index].Error.Code == "service_unavailable" {
				c.Header("Retry-After", "1")
			}
			failed = true
			continue
		}
		data := orderMailResponse(results[i].Items, results[i].Fetch)
		resp[index].Status = "succeeded"
		resp[index].Data = &data
	}
	status := http.StatusOK
	if failed {
		status = http.StatusMultiStatus
	}
	c.JSON(status, resp)
}

func normalizePickupClientIP(value string) string {
	value = strings.TrimSpace(value)
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return value
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

const (
	maxPickupBatchSize   = 200
	maxPickupBatchBytes  = 128 << 10
	maxPickupLimiterKeys = 100000
	pickupTimeout        = 9 * time.Second
	pickupMaxActive      = 16
	pickupMaxTotal       = 32
)

var (
	globalPickupListLimiter    = newPickupLimiter(rate.Limit(1), 3)
	globalPickupDetailLimiter  = newPickupLimiter(rate.Limit(10), 20)
	globalPickupBatchIPLimiter = newPickupLimiter(rate.Every(10*time.Second), 2)
	// ponytail: process-local limits match the current single app replica;
	// move these permits to Redis only when the app is horizontally scaled.
	globalPickupOutstanding = make(chan struct{}, pickupMaxTotal)
	globalPickupExecution   = make(chan struct{}, pickupMaxActive)
)

func acquirePickup(ctx context.Context) (func(), bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	select {
	case globalPickupOutstanding <- struct{}{}:
		observePickupState()
	default:
		return nil, false
	}
	select {
	case globalPickupExecution <- struct{}{}:
		observePickupState()
		return func() {
			<-globalPickupExecution
			<-globalPickupOutstanding
			observePickupState()
		}, true
	case <-ctx.Done():
		<-globalPickupOutstanding
		observePickupState()
		return nil, false
	}
}

func observePickupState() {
	active := len(globalPickupExecution)
	queued := max(len(globalPickupOutstanding)-active, 0)
	platform.SetWorkloadState("pickup", active, queued, queued)
}

func writePickupUnavailable(c *gin.Context) {
	c.Header("Retry-After", "1")
	c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": middleware.GetRequestID(c)})
}

type pickupLimiter struct {
	mu         sync.Mutex
	items      map[string]*pickupLimiterEntry
	limit      rate.Limit
	burst      int
	maxKeys    int
	lastPruned time.Time
}

type pickupLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newPickupLimiter(limit rate.Limit, burst int) *pickupLimiter {
	return &pickupLimiter{
		items:   make(map[string]*pickupLimiterEntry),
		limit:   limit,
		burst:   burst,
		maxKeys: maxPickupLimiterKeys,
	}
}

func (l *pickupLimiter) allow(token string) bool {
	key := pickupLimitKey(token)
	if key == "" {
		return false
	}
	now := time.Now()
	l.mu.Lock()
	if len(l.items) >= l.maxKeys && (l.lastPruned.IsZero() || now.Sub(l.lastPruned) >= time.Minute) {
		l.pruneLocked(now)
		l.lastPruned = now
	}
	entry := l.items[key]
	if entry == nil {
		if len(l.items) >= l.maxKeys {
			l.mu.Unlock()
			return false
		}
		entry = &pickupLimiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.items[key] = entry
	}
	entry.lastSeen = now
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

func validPickupCredential(credential mailmatchapp.PickupCredential) bool {
	if credential.Email == "" || len(credential.Email) > 254 || credential.Token == "" || len(credential.Token) > 255 {
		return false
	}
	address, err := stdmail.ParseAddress(credential.Email)
	return err == nil && strings.EqualFold(address.Address, credential.Email)
}

func pickupBatchItemError(err error) *PickupBatchItemErrorResponse {
	switch {
	case errors.Is(err, domain.ErrPickupCredentialInvalid):
		return &PickupBatchItemErrorResponse{Code: "credential_invalid", Message: "Credential is invalid or expired."}
	case errors.Is(err, domain.ErrOrderUnavailable):
		return &PickupBatchItemErrorResponse{Code: "order_unavailable", Message: "Order is not available for mail reading."}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, domain.ErrFetchQueueUnavailable), errors.Is(err, domain.ErrMailServiceUnavailable):
		return &PickupBatchItemErrorResponse{Code: "service_unavailable", Message: "Mail service is temporarily unavailable."}
	default:
		return &PickupBatchItemErrorResponse{Code: "internal_error", Message: "An unexpected error occurred."}
	}
}

func bindPickupBatchJSON(c *gin.Context, destination any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPickupBatchBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func writePickupBatchBodyError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	message := "Invalid request body."
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		status = http.StatusRequestEntityTooLarge
		message = "Request body is too large."
	}
	c.JSON(status, gin.H{"message": message, "requestId": middleware.GetRequestID(c)})
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
