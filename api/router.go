package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/donnel666/remail/api/health"
	"github.com/donnel666/remail/api/middleware"
	coreapi "github.com/donnel666/remail/internal/core/api"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamapi "github.com/donnel666/remail/internal/iam/api"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
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
	taskMux := asynq.NewServeMux()
	v1 := r.Group("/v1")
	{
		// IAM module (activation, auth, users)
		smtpDelivery := mailinfra.NewSMTPDelivery(mailinfra.SMTPConfig{
			Addr:     p.SMTP.Addr,
			Username: p.SMTP.Username,
			Password: p.SMTP.Password,
			From:     p.SMTP.From,
		})
		mailDelivery := mailapp.NewDeliveryService(mailinfra.NewOutboundMailStore(p.Redis), smtpDelivery)
		iamMod, err := iamapi.NewIAMModule(p.DB, p.Redis, mailDelivery)
		if err != nil {
			return nil, err
		}
		iamapi.RegisterIAMRoutes(v1, iamMod, p.SessionMaxAge, p.SessionSecure)

		// Core module (resources, mail servers, domains)
		fileStore := governanceinfra.NewMinIOFileStore(p.MinIO, p.MinIOBucket)
		coreMod, err := coreapi.NewCoreModule(p.DB, p.Redis, fileStore, p.Asynq)
		if err != nil {
			return nil, err
		}
		coreapi.RegisterCoreTaskHandlers(taskMux, coreMod)
		iamSessionFetcher := iamapi.NewSessionFetcher(iamMod.SessionStore, iamMod.UserRepo)
		coreapi.RegisterCoreRoutes(v1, coreMod, iamSessionFetcher)

		// Proxy module (admin proxy pool maintenance)
		proxyMod, err := proxyapi.NewProxyModule(p.DB, p.Asynq)
		if err != nil {
			return nil, err
		}
		proxyapi.RegisterProxyTaskHandlers(taskMux, proxyMod)
		proxyapi.RegisterProxyRoutes(v1, proxyMod, iamSessionFetcher, iamMod.PermissionChecker)
	}
	if err := p.AsynqServer.Start(taskMux); err != nil {
		return nil, err
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
