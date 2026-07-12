package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestIDPreservesSafeBoundedClientValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/request-id", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"requestId": GetRequestID(c)})
	})

	request := httptest.NewRequest(http.MethodGet, "/request-id", nil)
	request.Header.Set("X-Request-ID", "admin-resource:req_123.456")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, "admin-resource:req_123.456", response.Header().Get("X-Request-ID"))
	require.Contains(t, response.Body.String(), "admin-resource:req_123.456")
}

func TestRequestIDReplacesUnboundedOrUnsafeClientValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, supplied := range []string{
		strings.Repeat("x", maxRequestIDLength+1),
		"refresh-token-canary/unsafe",
		"非ASCII请求标识",
	} {
		router := gin.New()
		router.Use(RequestID())
		router.GET("/request-id", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"requestId": GetRequestID(c)})
		})

		request := httptest.NewRequest(http.MethodGet, "/request-id", nil)
		request.Header.Set("X-Request-ID", supplied)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		generated := response.Header().Get("X-Request-ID")
		require.NotEmpty(t, generated)
		require.NotEqual(t, supplied, generated)
		require.LessOrEqual(t, len(generated), maxRequestIDLength)
		require.NotContains(t, response.Body.String(), supplied)
		require.NotContains(t, response.Body.String(), "refresh-token-canary")
	}
}
