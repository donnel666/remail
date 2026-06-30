package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/donnel666/remail/api/health"
	"github.com/donnel666/remail/api/middleware"
	iamapi "github.com/donnel666/remail/internal/iam/api"
	iaminfra "github.com/donnel666/remail/internal/iam/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/gin-gonic/gin"
)

// SetupRouter creates the Gin engine with all middleware and route registrations.
// feFS is the embedded frontend dist filesystem (nil in development mode).
func SetupRouter(p *platform.Platform, feFS fs.FS) (*gin.Engine, error) {
	r := gin.New()

	// Global middleware
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.RequestLogger(p.Diagnostics.SlowRequestThreshold))
	r.Use(middleware.CORS("http://localhost:3000", "http://127.0.0.1:3000"))

	// Health check endpoints (outside /v1)
	h := health.NewHandler(p)
	r.GET("/healthz", h.Healthz)
	r.GET("/readyz", h.Readyz)

	// API v1 routes
	v1 := r.Group("/v1")
	{
		// IAM module (activation, auth, users)
		emailCodeSender := iaminfra.NewEmailCodeSender(iaminfra.EmailCodeSenderConfig{
			Addr:     p.SMTP.Addr,
			Username: p.SMTP.Username,
			Password: p.SMTP.Password,
			From:     p.SMTP.From,
		})
		iamMod, err := iamapi.NewIAMModule(p.DB, p.Redis, emailCodeSender)
		if err != nil {
			return nil, err
		}
		iamapi.RegisterIAMRoutes(v1, iamMod, p.SessionMaxAge, p.SessionSecure)
	}

	// Serve embedded frontend SPA if available
	if feFS != nil {
		serveEmbeddedFrontend(r, feFS)
	}

	return r, nil
}

// serveEmbeddedFrontend serves the SPA frontend from the embedded filesystem.
func serveEmbeddedFrontend(r *gin.Engine, feFS fs.FS) {
	if staticFS, err := fs.Sub(feFS, "static"); err == nil {
		r.StaticFS("/static", http.FS(staticFS))
	}

	r.NoRoute(func(c *gin.Context) {
		urlPath := c.Request.URL.Path
		if strings.HasPrefix(urlPath, "/v1/") || strings.HasPrefix(urlPath, "/healthz") || strings.HasPrefix(urlPath, "/readyz") {
			c.Status(http.StatusNotFound)
			return
		}

		if serveFrontendFile(c, feFS, urlPath) {
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

func serveFrontendFile(c *gin.Context, feFS fs.FS, urlPath string) bool {
	name := strings.TrimPrefix(path.Clean(urlPath), "/")
	if name == "." || name == "" {
		return false
	}

	info, err := fs.Stat(feFS, name)
	if err != nil || info.IsDir() {
		return false
	}

	c.FileFromFS(name, http.FS(feFS))
	return true
}
