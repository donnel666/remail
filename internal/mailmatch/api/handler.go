package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	stdmail "net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
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
	ctx := c.Request.Context()
	serviceStarted := time.Now()
	serviceResult := "succeeded"
	defer func() { platform.ObserveServiceDuration("pickup_single", "single", serviceResult, serviceStarted) }()
	items, state, err := h.mod.UseCase.ListPickupMail(ctx, tokenPlain, email)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			serviceResult = "canceled"
			writePickupUnavailable(c)
			return
		}
		if errors.Is(err, domain.ErrPickupCredentialInvalid) || errors.Is(err, domain.ErrOrderUnavailable) {
			serviceResult = "business_failed"
		} else {
			serviceResult = "system_failed"
		}
		writeMailmatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderMailResponse(items, state))
}

func (h *Handler) PostPickupMessagesBatch(c *gin.Context) {
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
	succeededItems, businessFailedItems, systemFailedItems := 0, 0, 0
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
			businessFailedItems++
			continue
		}
		credentials = append(credentials, credential)
		credentialIndexes = append(credentialIndexes, i)
	}
	ctx := c.Request.Context()
	serviceStarted := time.Now()
	serviceResult := "succeeded"
	defer func() {
		platform.ObserveServiceDuration("pickup_batch", pickupServiceSize(len(req.Items)), serviceResult, serviceStarted)
	}()
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
			if errors.Is(results[i].Err, domain.ErrPickupCredentialInvalid) || errors.Is(results[i].Err, domain.ErrOrderUnavailable) {
				businessFailedItems++
			} else {
				systemFailedItems++
			}
			continue
		}
		data := orderMailResponse(results[i].Items, results[i].Fetch)
		resp[index].Status = "succeeded"
		resp[index].Data = &data
		succeededItems++
	}
	size := pickupServiceSize(len(req.Items))
	platform.AddWorkUnits("pickup_batch", size, "requested", len(req.Items))
	platform.AddWorkUnits("pickup_batch", size, "succeeded", succeededItems)
	platform.AddWorkUnits("pickup_batch", size, "business_failed", businessFailedItems)
	platform.AddWorkUnits("pickup_batch", size, "system_failed", systemFailedItems)
	platform.AddWorkUnits(
		"pickup_batch", size, "unprocessed",
		len(req.Items)-succeededItems-businessFailedItems-systemFailedItems,
	)
	status := http.StatusOK
	if failed {
		status = http.StatusMultiStatus
	}
	serviceResult = pickupBatchServiceResult(succeededItems, businessFailedItems, systemFailedItems)
	c.JSON(status, resp)
}

func pickupBatchServiceResult(succeeded, businessFailed, systemFailed int) string {
	switch {
	case businessFailed == 0 && systemFailed == 0:
		return "succeeded"
	case succeeded == 0 && businessFailed > 0 && systemFailed == 0:
		return "business_failed"
	case succeeded == 0 && systemFailed > 0 && businessFailed == 0:
		return "system_failed"
	default:
		return "partial"
	}
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
	maxPickupBatchSize  = 200
	maxPickupBatchBytes = 128 << 10
)

func pickupServiceSize(quantity int) string {
	switch {
	case quantity <= 1:
		return "single"
	case quantity <= 20:
		return "002_020"
	case quantity <= 50:
		return "021_050"
	case quantity <= 100:
		return "051_100"
	default:
		return "101_200"
	}
}

func writePickupUnavailable(c *gin.Context) {
	c.Header("Retry-After", "1")
	c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": middleware.GetRequestID(c)})
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
