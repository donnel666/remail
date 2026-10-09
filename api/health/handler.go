package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/iam/domain"
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
// With ready=1 it checks dependencies but returns only a generic status.
func (h *Handler) Healthz(c *gin.Context) {
	if c.Query("ready") == "1" {
		h.readiness(c, false)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Readyz returns the readiness status. It checks all external dependencies.
// Returns 200 if all dependencies are healthy, 503 otherwise.
// Error details are logged internally but never exposed in the HTTP response.
func (h *Handler) Readyz(c *gin.Context) {
	h.readiness(c, true)
}

func (h *Handler) readiness(c *gin.Context, details bool) {
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

	if !h.platform.WorkersReady() {
		deps["workers"] = "unhealthy"
		allHealthy = false
	} else {
		deps["workers"] = "healthy"
	}

	statusCode := http.StatusOK
	status := "ok"
	if !allHealthy {
		statusCode = http.StatusServiceUnavailable
		status = "error"
	}
	response := gin.H{"status": status}
	if details {
		response["dependencies"] = deps
	}
	c.JSON(statusCode, response)
}

// RegisterMonitoringRoutes restricts operational data to logged-in super administrators.
func RegisterMonitoringRoutes(r *gin.Engine, p *platform.Platform, fetcher middleware.SessionFetcher) {
	monitoring := r.Group("", middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.RoleRequired(domain.RoleSuperAdmin))
	monitoring.GET("/readyz", NewHandler(p).Readyz)
	monitoring.GET("/metrics", gin.WrapH(platform.MetricsHandler()))
}
