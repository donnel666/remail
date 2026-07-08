package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapidomain "github.com/donnel666/remail/internal/openapi/domain"
	"github.com/gin-gonic/gin"
)

const (
	contextKeyClientChannel = "openapi_client_channel"
	contextKeyAPIKeyID      = "openapi_api_key_id"
	contextKeyAPIKeyAllowed = "openapi_api_key_allowed"

	ClientChannelConsole = "console"
	ClientChannelAPIKey  = "api_key"
)

func LoadAPIKey(useCase *openapiapp.UseCase) gin.HandlerFunc {
	return func(c *gin.Context) {
		plain, authHeaderPresent, validAuthHeader := apiKeyCredential(c)
		if !authHeaderPresent && plain == "" {
			c.Next()
			return
		}
		if authHeaderPresent && !validAuthHeader {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
			return
		}
		if authHeaderPresent && !strings.HasPrefix(plain, "ak_") {
			c.Next()
			return
		}
		result, err := useCase.BeginAPIKeyRequest(c.Request.Context(), plain)
		if err != nil {
			writeAuthError(c, err)
			return
		}
		middleware.SetCurrentUser(c, result.UserID, iamdomain.RoleUser, "", "")
		c.Set(contextKeyClientChannel, ClientChannelAPIKey)
		c.Set(contextKeyAPIKeyID, result.APIKeyID)
		startedAt := time.Now()
		defer func() {
			logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = useCase.LogAPIRequest(logCtx, openapiapp.LogAPIRequestRequest{
				PrincipalType:  "api_key",
				PrincipalID:    result.APIKeyID,
				UserID:         result.UserID,
				Path:           apiLogPath(c),
				Method:         c.Request.Method,
				IdempotencyKey: c.GetHeader("Idempotency-Key"),
				HTTPStatus:     c.Writer.Status(),
				DurationMs:     int(time.Since(startedAt) / time.Millisecond),
				RequestID:      middleware.GetRequestID(c),
			})
			cancel()

			finishCtx, finishCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer finishCancel()
			_ = useCase.FinishAPIKeyRequest(finishCtx, result.APIKeyID)
		}()
		c.Next()
	}
}

func MarkConsoleChannel() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := c.Get(contextKeyClientChannel); !ok {
			c.Set(contextKeyClientChannel, ClientChannelConsole)
		}
		c.Next()
	}
}

func KeyAllowed() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(contextKeyAPIKeyAllowed, true)
		c.Next()
	}
}

func RequirePrincipalAllowed() gin.HandlerFunc {
	return func(c *gin.Context) {
		if channel, ok := CurrentClientChannel(c); ok && channel == ClientChannelAPIKey && !isAPIKeyAllowed(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
			return
		}
		c.Next()
	}
}

func CSRFRequiredForSession() gin.HandlerFunc {
	csrf := middleware.CSRFRequired()
	return func(c *gin.Context) {
		if channel, ok := CurrentClientChannel(c); ok && channel != ClientChannelConsole {
			c.Next()
			return
		}
		csrf(c)
	}
}

func SessionOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if channel, ok := CurrentClientChannel(c); ok && channel != ClientChannelConsole {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
			return
		}
		c.Next()
	}
}

func CurrentClientChannel(c *gin.Context) (string, bool) {
	value, ok := c.Get(contextKeyClientChannel)
	if !ok {
		return "", false
	}
	channel, ok := value.(string)
	return channel, ok
}

func CurrentAPIKeyID(c *gin.Context) (uint, bool) {
	value, ok := c.Get(contextKeyAPIKeyID)
	if !ok {
		return 0, false
	}
	id, ok := value.(uint)
	return id, ok
}

func isAPIKeyAllowed(c *gin.Context) bool {
	value, ok := c.Get(contextKeyAPIKeyAllowed)
	if !ok {
		return false
	}
	allowed, ok := value.(bool)
	return ok && allowed
}

func writeAuthError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, openapidomain.ErrAPIKeyDisabled), errors.Is(err, openapidomain.ErrAPIKeyExpired), errors.Is(err, openapidomain.ErrAPIKeyNotFound), errors.Is(err, openapidomain.ErrInvalidAPIKey):
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": requestID})
	case errors.Is(err, openapidomain.ErrAPIKeyRateLimited), errors.Is(err, openapidomain.ErrAPIKeyQuotaExceeded):
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"message": "Too many requests.", "requestId": requestID})
	case errors.Is(err, openapidomain.ErrAPIKeyConcurrencyLimit):
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "Credential concurrency limit reached.", "requestId": requestID})
	default:
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}

func apiKeyCredential(c *gin.Context) (plain string, authHeaderPresent bool, validAuthHeader bool) {
	if plain, present, valid := bearerCredential(c); present {
		return plain, true, valid
	}
	return strings.TrimSpace(c.GetHeader("X-API-Key")), false, true
}

func bearerCredential(c *gin.Context) (plain string, present bool, valid bool) {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if auth != "" {
		parts := strings.Fields(auth)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", true, false
		}
		return strings.TrimSpace(parts[1]), true, true
	}
	return "", false, true
}

func apiLogPath(c *gin.Context) string {
	if fullPath := strings.TrimSpace(c.FullPath()); fullPath != "" {
		return fullPath
	}
	if c.Request == nil || c.Request.URL == nil {
		return ""
	}
	return c.Request.URL.Path
}
