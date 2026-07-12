package middleware

import (
	"github.com/gin-gonic/gin"
)

// CORS returns a middleware that handles CORS for development.
// In production, the frontend is embedded in the Go binary so no CORS is needed.
func CORS(allowedOrigins ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		// Allow if no restrictions or origin is in allowed list
		allow := len(allowedOrigins) == 0
		for _, o := range allowedOrigins {
			if o == origin || o == "*" {
				allow = true
				break
			}
		}

		if allow {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID, X-CSRF-Token, Idempotency-Key, X-API-Key")
			c.Header("Access-Control-Max-Age", "86400")
		}

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
