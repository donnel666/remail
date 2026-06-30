package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
)

// Handler holds dependencies for health check endpoints.
type Handler struct {
	platform *platform.Platform
}

// NewHandler creates a new health check handler.
func NewHandler(p *platform.Platform) *Handler {
	return &Handler{platform: p}
}

// Healthz returns a simple liveness probe. It does not check dependencies.
// Use this for basic "is the process alive" checks.
func (h *Handler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Readyz returns the readiness status. It checks all external dependencies.
// Returns 200 if all dependencies are healthy, 503 otherwise.
// Error details are logged internally but never exposed in the HTTP response.
func (h *Handler) Readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	deps := make(map[string]string)
	allHealthy := true

	// Check MySQL
	if err := h.platform.SQLDB.PingContext(ctx); err != nil {
		slog.Warn("mysql readiness check failed", "error", err)
		deps["mysql"] = "unhealthy"
		allHealthy = false
	} else {
		deps["mysql"] = "healthy"
	}

	// Check Redis
	if err := h.platform.Redis.Ping(ctx).Err(); err != nil {
		slog.Warn("redis readiness check failed", "error", err)
		deps["redis"] = "unhealthy"
		allHealthy = false
	} else {
		deps["redis"] = "healthy"
	}

	// Check MinIO connectivity
	if _, err := h.platform.MinIO.ListBuckets(ctx); err != nil {
		slog.Warn("minio readiness check failed", "error", err)
		deps["minio"] = "unhealthy"
		allHealthy = false
	} else {
		deps["minio"] = "healthy"
	}

	if !allHealthy {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":       "error",
			"dependencies": deps,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":       "ok",
		"dependencies": deps,
	})
}
