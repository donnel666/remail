package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/gin-gonic/gin"
)

type adminMessageSummaryResponse struct {
	ID               uint      `json:"id"`
	Mailbox          string    `json:"mailbox"`
	Recipient        string    `json:"recipient"`
	Sender           string    `json:"sender"`
	Subject          string    `json:"subject"`
	Preview          string    `json:"preview"`
	Status           string    `json:"status"`
	VerificationCode *string   `json:"verificationCode"`
	OrderNo          *string   `json:"orderNo"`
	ReceivedAt       time.Time `json:"receivedAt"`
}

type adminMessageDetailResponse struct {
	adminMessageSummaryResponse
	Body            string  `json:"body"`
	MatchDiagnostic *string `json:"matchDiagnostic"`
}

type adminMessageListResponse struct {
	Items  []adminMessageSummaryResponse `json:"items"`
	Total  int64                         `json:"total"`
	Offset int                           `json:"offset"`
	Limit  int                           `json:"limit"`
}

func (h *Handler) GetAdminMessages(c *gin.Context) {
	if h == nil || h.mod == nil || h.mod.AdminMessages == nil {
		writeAdminMessageUnavailable(c)
		return
	}
	resourceID, ok := positiveUintQuery(c, "resourceId")
	if !ok {
		writeAdminMessageBadRequest(c)
		return
	}
	resourceType, ok := adminMessageResourceType(c)
	if !ok {
		writeAdminMessageBadRequest(c)
		return
	}
	offset, ok := nonNegativeIntQuery(c, "offset", 0)
	if !ok {
		writeAdminMessageBadRequest(c)
		return
	}
	limit, ok := nonNegativeIntQuery(c, "limit", 20)
	if !ok || limit == 0 {
		writeAdminMessageBadRequest(c)
		return
	}
	page, err := h.mod.AdminMessages.List(c.Request.Context(), mailmatchapp.AdminMessageListQuery{
		ResourceID: resourceID, ResourceType: resourceType, Search: c.Query("search"), Offset: offset, Limit: limit,
	})
	if err != nil {
		writeAdminMessageError(c, err)
		return
	}
	items := make([]adminMessageSummaryResponse, len(page.Items))
	for i := range page.Items {
		items[i] = adminMessageSummaryDTO(page.Items[i])
	}
	c.JSON(http.StatusOK, adminMessageListResponse{
		Items:  items,
		Total:  page.Total,
		Offset: page.Offset,
		Limit:  page.Limit,
	})
}

func (h *Handler) GetAdminMessage(c *gin.Context) {
	if h == nil || h.mod == nil || h.mod.AdminMessages == nil {
		writeAdminMessageUnavailable(c)
		return
	}
	resourceID, ok := positiveUintQuery(c, "resourceId")
	if !ok {
		writeAdminMessageBadRequest(c)
		return
	}
	resourceType, ok := adminMessageResourceType(c)
	if !ok {
		writeAdminMessageBadRequest(c)
		return
	}
	messageID64, err := strconv.ParseUint(strings.TrimSpace(c.Param("messageId")), 10, 64)
	if err != nil || messageID64 == 0 {
		writeAdminMessageBadRequest(c)
		return
	}
	operatorUserID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	detail, err := h.mod.AdminMessages.Get(
		c.Request.Context(),
		operatorUserID,
		resourceID,
		resourceType,
		uint(messageID64),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeAdminMessageError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminMessageDetailResponse{
		adminMessageSummaryResponse: adminMessageSummaryDTO(detail.AdminMessageSummary),
		Body:                        detail.Body,
		MatchDiagnostic:             detail.MatchDiagnostic,
	})
}

func adminMessageResourceType(c *gin.Context) (domain.ResourceType, bool) {
	value := domain.ResourceType(strings.TrimSpace(c.DefaultQuery("type", string(domain.ResourceTypeMicrosoft))))
	return value, value == domain.ResourceTypeMicrosoft || value == domain.ResourceTypeDomain
}

func adminMessageSummaryDTO(item mailmatchapp.AdminMessageSummary) adminMessageSummaryResponse {
	return adminMessageSummaryResponse{
		ID:               item.ID,
		Mailbox:          item.Mailbox,
		Recipient:        item.Recipient,
		Sender:           item.Sender,
		Subject:          item.Subject,
		Preview:          item.Preview,
		Status:           string(item.Status),
		VerificationCode: item.VerificationCode,
		OrderNo:          item.OrderNo,
		ReceivedAt:       item.ReceivedAt,
	}
}

func positiveUintQuery(c *gin.Context, name string) (uint, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(c.Query(name)), 10, 64)
	return uint(value), err == nil && value > 0
}

func nonNegativeIntQuery(c *gin.Context, name string, fallback int) (int, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil && value >= 0
}

func writeAdminMessageBadRequest(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"message":   "Invalid request parameters.",
		"requestId": middleware.GetRequestID(c),
	})
}

func writeAdminMessageUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"message":   "Mail service is temporarily unavailable.",
		"requestId": middleware.GetRequestID(c),
	})
}

func writeAdminMessageError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, domain.ErrAdminMessageResourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrMessageNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Message not found.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
