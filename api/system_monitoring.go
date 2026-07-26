package api

import (
	"context"
	"net/http"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
)

func systemMonitoringHandler(p *platform.Platform) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		c.JSON(http.StatusOK, p.MonitoringSnapshot(ctx))
	}
}
