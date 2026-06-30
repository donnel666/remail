package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	SessionCookieName = "sid"
	CSRFCookieName    = "csrf_token"
	CSRFHeaderName    = "X-CSRF-Token"
)

func CSRFRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}

		cookieToken, cookieErr := c.Cookie(CSRFCookieName)
		headerToken := c.GetHeader(CSRFHeaderName)
		if cookieErr != nil || cookieToken == "" || headerToken == "" || !constantTimeEqual(cookieToken, headerToken) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"message":   "Permission denied.",
				"requestId": GetRequestID(c),
			})
			return
		}

		c.Next()
	}
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
