package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/gin-gonic/gin"
)

const (
	SystemKeyHeaderName   = "X-System-Key"
	contextKeySystemKeyID = "system_key_id"
)

type SystemKeyAuthenticator interface {
	AuthenticateSystemKey(ctx context.Context, plain string) (uint, error)
}

func SystemKeyRequired(authenticator SystemKeyAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authenticator == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"message": "Service is temporarily unavailable.", "requestId": GetRequestID(c),
			})
			return
		}
		keyID, err := authenticator.AuthenticateSystemKey(c.Request.Context(), strings.TrimSpace(c.GetHeader(SystemKeyHeaderName)))
		if err != nil {
			if errors.Is(err, settingsdomain.ErrInvalidSystemKey) || errors.Is(err, settingsdomain.ErrSystemKeyNotFound) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"message": "Authentication is required.", "requestId": GetRequestID(c),
				})
				return
			}
			slog.Error("system key lookup failed", "request_id", GetRequestID(c), "error", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"message": "Service is temporarily unavailable.", "requestId": GetRequestID(c),
			})
			return
		}
		c.Set(contextKeySystemKeyID, keyID)
		c.Next()
	}
}

func GetCurrentSystemKeyID(c *gin.Context) (uint, bool) {
	value, exists := c.Get(contextKeySystemKeyID)
	if !exists {
		return 0, false
	}
	keyID, ok := value.(uint)
	return keyID, ok && keyID != 0
}
