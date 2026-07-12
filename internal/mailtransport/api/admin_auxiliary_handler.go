package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/gin-gonic/gin"
)

type MailTransportHandler struct {
	module *MailTransportModule
}

func NewMailTransportHandler(module *MailTransportModule) *MailTransportHandler {
	return &MailTransportHandler{module: module}
}

func (h *MailTransportHandler) GetAdminBindings(c *gin.Context) {
	resourceID, ok := parsePositiveAdminMailUint(c.Query("resourceId"))
	if !ok {
		writeAdminMailBadRequest(c)
		return
	}
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err == nil && parsedLimit > mailapp.AuxiliaryMailMaxLimit {
			writeAdminMailBadRequest(c)
			return
		}
	}
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{
		DefaultLimit: mailapp.AuxiliaryMailDefaultLimit,
		MaxLimit:     mailapp.AuxiliaryMailMaxLimit,
	})
	if !ok {
		return
	}
	if h == nil || h.module == nil || h.module.AuxiliaryMail == nil {
		writeAdminMailError(c, domain.ErrAuxiliaryMailUnavailable)
		return
	}
	page, err := h.module.AuxiliaryMail.List(c.Request.Context(), mailapp.AuxiliaryMailFilter{
		ResourceID: resourceID,
		Search:     strings.TrimSpace(c.Query("search")),
		Offset:     offset,
		Limit:      limit,
	})
	if err != nil {
		writeAdminMailError(c, err)
		return
	}
	items := make([]AdminAuxiliaryMessageSummaryResponse, len(page.Items))
	for i := range page.Items {
		items[i] = adminAuxiliaryMessageSummaryResponse(page.Items[i])
	}
	var binding *AdminBindingSummaryResponse
	if page.Binding != nil {
		binding = &AdminBindingSummaryResponse{
			ID:           page.Binding.ID,
			EmailAddress: page.Binding.EmailAddress,
			Status:       string(page.Binding.Status),
			UpdatedAt:    page.Binding.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, AdminBindingMessageListResponse{
		Binding: binding,
		Items:   items,
		Total:   page.Total,
		Offset:  page.Offset,
		Limit:   page.Limit,
	})
}

func (h *MailTransportHandler) GetAdminBindingMessage(c *gin.Context) {
	resourceID, ok := parsePositiveAdminMailUint(c.Query("resourceId"))
	if !ok {
		writeAdminMailBadRequest(c)
		return
	}
	messageID, ok := parsePositiveAdminMailUint(c.Param("messageId"))
	if !ok {
		writeAdminMailBadRequest(c)
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
	if h == nil || h.module == nil || h.module.AuxiliaryMail == nil {
		writeAdminMailError(c, domain.ErrAuxiliaryMailUnavailable)
		return
	}
	detail, err := h.module.AuxiliaryMail.Get(c.Request.Context(), mailapp.AuxiliaryMailDetailRequest{
		ResourceID:     resourceID,
		MessageID:      messageID,
		OperatorUserID: operatorUserID,
		RequestID:      middleware.GetRequestID(c),
		Path:           c.FullPath(),
	})
	if err != nil {
		writeAdminMailError(c, err)
		return
	}
	c.JSON(http.StatusOK, AdminAuxiliaryMessageDetailResponse{
		AdminAuxiliaryMessageSummaryResponse: adminAuxiliaryMessageSummaryResponse(detail.AuxiliaryMessageSummary),
		Body:                                 detail.Body,
		MatchDiagnostic:                      detail.MatchDiagnostic,
	})
}

func adminAuxiliaryMessageSummaryResponse(item mailapp.AuxiliaryMessageSummary) AdminAuxiliaryMessageSummaryResponse {
	return AdminAuxiliaryMessageSummaryResponse{
		ID:               item.ID,
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

func parsePositiveAdminMailUint(value string) (uint, bool) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed == 0 {
		return 0, false
	}
	return uint(parsed), true
}

func writeAdminMailBadRequest(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"message":   "Invalid request parameters.",
		"requestId": middleware.GetRequestID(c),
	})
}

func writeAdminMailError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrInvalidAuxiliaryMailQuery):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid query parameters.", "requestId": requestID})
	case mailapp.IsAuxiliaryMailNotFound(err):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrAuxiliaryMailUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
