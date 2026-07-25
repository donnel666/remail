package middleware

import (
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
)

const defaultSlowRequestThreshold = time.Second

func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		elapsed := time.Since(start)
		slowThreshold := runtimeconfig.Duration("slow_request_threshold_ms", defaultSlowRequestThreshold, time.Millisecond, 0)
		if shouldSkipRequestLog(c.Request.URL.Path, elapsed, slowThreshold) {
			return
		}

		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"route", c.FullPath(),
			"status", c.Writer.Status(),
			"latency_ms", elapsed.Seconds() * 1000,
			"client_ip", c.ClientIP(),
			"bytes", c.Writer.Size(),
		}

		logger := platform.Logger(c.Request.Context())
		if slowThreshold > 0 && elapsed > slowThreshold {
			logger.Warn("slow http request", append(attrs, "threshold_ms", slowThreshold.Seconds()*1000)...)
			return
		}
		logger.Info("http request", attrs...)
	}
}

func shouldSkipRequestLog(requestPath string, elapsed time.Duration, slowThreshold time.Duration) bool {
	if slowThreshold > 0 && elapsed > slowThreshold {
		return false
	}
	if requestPath == "/healthz" || requestPath == "/readyz" || requestPath == "/favicon.ico" {
		return true
	}
	return strings.HasPrefix(requestPath, "/static/")
}
