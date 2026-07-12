package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCORSTrustedOriginAllowsRequiredAdminCommandHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS("http://localhost:3000"))
	router.OPTIONS("/v1/admin/resources/42/disable", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodOptions, "/v1/admin/resources/42/disable", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "X-CSRF-Token, Idempotency-Key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, "http://localhost:3000", response.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header().Get("Access-Control-Allow-Credentials"))
	require.Contains(t, response.Header().Get("Access-Control-Allow-Headers"), "X-CSRF-Token")
	require.Contains(t, response.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key")
}

func TestCORSUntrustedOriginDoesNotReceiveCredentialHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS("http://localhost:3000"))
	router.OPTIONS("/v1/admin/resources/42/disable", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodOptions, "/v1/admin/resources/42/disable", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Empty(t, response.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, response.Header().Get("Access-Control-Allow-Credentials"))
	require.Empty(t, response.Header().Get("Access-Control-Allow-Headers"))
}
