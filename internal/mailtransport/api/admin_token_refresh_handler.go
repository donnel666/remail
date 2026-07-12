package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/donnel666/remail/api/middleware"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/gin-gonic/gin"
)

func (h *MailTransportHandler) PostAdminMicrosoftResourceTokenRefresh(c *gin.Context) {
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
	if h == nil || h.module == nil || h.module.TokenRefresh == nil {
		writeAdminTokenRefreshError(c, mailapp.ErrMicrosoftTokenRefreshUnavailable)
		return
	}

	result, err := h.module.TokenRefresh.Accept(c.Request.Context(), mailapp.MicrosoftTokenRefreshCommand{
		ResourceID:     resourceID,
		OperatorUserID: operatorUserID,
		IdempotencyKey: idempotencyKey,
		RequestID:      middleware.GetRequestID(c),
		Path:           c.FullPath(),
	})
	if err != nil {
		writeAdminTokenRefreshError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, adminMailTransportTaskAcceptedDTO(result))
}

func writeAdminTokenRefreshError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, mailapp.ErrInvalidMicrosoftTokenRefresh):
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request parameters.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftTokenRefreshNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftAdminIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency-Key conflicts with a different request.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftTokenRefreshConflict),
		errors.Is(err, mailapp.ErrMicrosoftTokenRefreshStale):
		c.JSON(http.StatusConflict, gin.H{"message": "Resource state does not allow token refresh.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftTokenCredentialsMissing):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Microsoft token credentials are incomplete.", "requestId": requestID})
	case errors.Is(err, mailapp.ErrMicrosoftTokenRefreshUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Mail service is temporarily unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}
