package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/donnel666/remail/api/health"
	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/platform"
)

// SetupRouter creates the Gin engine with all middleware and route registrations.
// frontendFS is the embedded frontend dist filesystem (nil in development mode).
func SetupRouter(p *platform.Platform, frontendFS fs.FS) *gin.Engine {
	r := gin.New()

	// Global middleware
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.CORS("http://localhost:3000", "http://127.0.0.1:3000"))

	// Health check endpoints
	h := health.NewHandler(p)
	RegisterHandlers(r, h)

	// Serve embedded frontend SPA if available
	if frontendFS != nil {
		serveEmbeddedFrontend(r, frontendFS)
	}

	return r
}

// serveEmbeddedFrontend serves the SPA frontend from the embedded filesystem.
func serveEmbeddedFrontend(r *gin.Engine, feFS fs.FS) {
	// Rsbuild outputs to:  dist/index.html  dist/static/js/...  dist/static/css/...
	// The HTML references /static/js/... so we need to serve /static from the embedded fs.
	if staticFS, err := fs.Sub(feFS, "static"); err == nil {
		r.StaticFS("/static", http.FS(staticFS))
	}

	// SPA fallback: serve index.html for all non-API routes
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		// Don't serve index.html for API routes or health checks
		if strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/healthz") || strings.HasPrefix(path, "/readyz") {
			c.Status(http.StatusNotFound)
			return
		}

		data, err := fs.ReadFile(feFS, "index.html")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})
}
