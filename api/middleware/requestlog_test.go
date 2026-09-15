package middleware

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestLoggerRedactsSMSLinkTokens(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())
	router.GET("/sms/:token", func(c *gin.Context) { c.String(200, "No message|2026-11-29") })
	for _, path := range []string{"/sms/secret-pickup-token", "/sms/secret-pickup-token/invalid"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	if strings.Contains(logs.String(), "secret-pickup-token") || !strings.Contains(logs.String(), "/sms/:token") {
		t.Fatalf("SMS link leaked into request log: %s", logs.String())
	}
}
