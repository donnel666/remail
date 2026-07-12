package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/gin-gonic/gin"
)

func (h *MailTransportHandler) PostAdminMicrosoftResourceAlias(c *gin.Context) {
	resourceID, ok := parsePositiveAdminMailUint(c.Param("resourceId"))
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
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Idempotency-Key is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if len(idempotencyKey) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid Idempotency-Key.",
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	if h == nil || h.module == nil || h.module.MicrosoftAliases == nil {
		writeAdminAliasExpediteError(c, mailapp.ErrMicrosoftAliasAdminUnavailable)
		return
	}

	result, err := h.module.MicrosoftAliases.AcceptAdminExpedite(
		c.Request.Context(),
		mailapp.MicrosoftAliasExpediteCommand{
			ResourceID:     resourceID,
			OperatorUserID: operatorUserID,
			IdempotencyKey: idempotencyKey,
			RequestID:      middleware.GetRequestID(c),
			Path:           c.FullPath(),
		},
	)
	if err != nil {
		writeAdminAliasExpediteError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, adminMailTransportTaskAcceptedDTO(result))
}

func writeAdminAliasExpediteError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, mailapp.ErrInvalidMicrosoftAliasExpedite):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftAliasResourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftAliasIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency-Key conflicts with a different request.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftAliasResourceConflict),
		errors.Is(err, mailapp.ErrMicrosoftAliasScheduleNotFound),
		errors.Is(err, mailapp.ErrMicrosoftAliasSchedulePaused):
		c.JSON(http.StatusConflict, gin.H{"message": "Resource state does not allow alias creation.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftAliasAdminUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
