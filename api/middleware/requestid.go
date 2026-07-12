package middleware

import (
	"strings"

	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
)

const maxRequestIDLength = 64

// RequestID returns a middleware that ensures every request has an X-Request-ID.
// If the client sends one, it is used; otherwise a new UUID is generated.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if !validRequestID(rid) {
			rid = platform.NewUUIDV7String()
		}

		c.Set("request_id", rid)
		c.Header("X-Request-ID", rid)

		// Propagate to request context for slog
		ctx := platform.WithRequestID(c.Request.Context(), rid)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLength {
		return false
	}
	for i := range len(value) {
		character := value[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}

// GetRequestID retrieves the request ID from the gin context.
func GetRequestID(c *gin.Context) string {
	if rid, ok := c.Get("request_id"); ok {
		if s, ok := rid.(string); ok {
			return s
		}
	}
	return ""
}
