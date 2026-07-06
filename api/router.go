package api

import (
	"context"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/donnel666/remail/api/health"
	"github.com/donnel666/remail/api/middleware"
	allocapi "github.com/donnel666/remail/internal/alloc/api"
	coreapi "github.com/donnel666/remail/internal/core/api"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamapi "github.com/donnel666/remail/internal/iam/api"
	mailapi "github.com/donnel666/remail/internal/mailtransport/api"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
)

// SetupRouter creates the Gin engine with all middleware and route registrations.
// feFS is the embedded frontend dist filesystem (nil in development mode).
func SetupRouter(p *platform.Platform, feFS fs.FS) (*gin.Engine, func(context.Context), error) {
	r := gin.New()
	cleanup := func(context.Context) {}

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
	var mailMod *mailapi.MailTransportModule
	v1 := r.Group("/v1")
	{
		// IAM module (activation, auth, users)
		fileStore := governanceinfra.NewMinIOFileStore(p.MinIO, p.MinIOBucket)

		// Proxy module is initialized before MailTransport so Microsoft ACL can
		// use the proxy pool through a port instead of bypassing BC-PROXY.
		proxyMod, err := proxyapi.NewProxyModule(p.DB, p.Asynq)
		if err != nil {
			return nil, cleanup, err
		}

		sender, err := mailSender(p.SMTP)
		if err != nil {
			return nil, cleanup, err
		}
		mailMod, err = mailapi.NewMailTransportModule(
			p.DB,
			fileStore,
			p.Asynq,
			sender,
			outboundSender(p.SMTP),
			mailinfra.InboundSMTPConfig{
				Enabled:         p.SMTP.InboundEnabled,
				Addr:            p.SMTP.InboundAddr,
				Domain:          p.SMTP.InboundDomain,
				MaxMessageBytes: p.SMTP.InboundMaxMessageBytes,
				MaxRecipients:   p.SMTP.InboundMaxRecipients,
				ReadTimeout:     p.SMTP.InboundReadTimeout,
				WriteTimeout:    p.SMTP.InboundWriteTimeout,
			},
			proxyMod.ProxyUseCase,
		)
		if err != nil {
			return nil, cleanup, err
		}
		mailapi.RegisterMailTransportTaskHandlers(taskMux, mailMod)

		iamMod, err := iamapi.NewIAMModule(p.DB, p.Redis, mailMod.DeliveryUseCase)
		if err != nil {
			return nil, cleanup, err
		}
		iamapi.RegisterIAMRoutes(v1, iamMod, p.SessionMaxAge, p.SessionSecure)

		// Core module (resources, mail servers, domains)
		coreMod, err := coreapi.NewCoreModule(p.DB, p.Redis, fileStore, p.Asynq, mailMod.ValidationUseCase, mailMod.BindingRecorder)
		if err != nil {
			return nil, cleanup, err
		}
		coreapi.RegisterCoreTaskHandlers(taskMux, coreMod)
		iamSessionFetcher := iamapi.NewSessionFetcher(iamMod.SessionStore, iamMod.UserRepo)
		coreapi.RegisterCoreRoutes(v1, coreMod, iamSessionFetcher, iamMod.PermissionChecker)

		// Allocation module (admin diagnostics and Trade-facing application port)
		allocMod := allocapi.NewModule(p.DB, p.Asynq)
		allocapi.RegisterAllocationTaskHandlers(taskMux, allocMod)
		allocapi.RegisterRoutes(v1, allocMod, iamSessionFetcher, iamMod.PermissionChecker)

		// Proxy module (admin proxy pool maintenance)
		proxyapi.RegisterProxyTaskHandlers(taskMux, proxyMod)
		proxyapi.RegisterProxyRoutes(v1, proxyMod, iamSessionFetcher, iamMod.PermissionChecker)
	}
	if err := p.AsynqServer.Start(taskMux); err != nil {
		cleanup(context.Background())
		return nil, cleanup, err
	}
	if mailMod != nil {
		cleanup = mailMod.Start(context.Background())
	}

	// Serve embedded frontend SPA if available
	if feFS != nil {
		serveEmbeddedFrontend(r, feFS)
	}

	return r, cleanup, nil
}

func mailSender(cfg platform.SMTPConfig) (mailapp.SenderPort, error) {
	dkimSigner, err := mailinfra.NewDKIMSigner(mailinfra.DKIMConfig{
		Enabled:        cfg.DKIMEnabled,
		Domain:         cfg.DKIMDomain,
		Selector:       cfg.DKIMSelector,
		Algorithm:      cfg.DKIMAlgorithm,
		Identity:       cfg.DKIMIdentity,
		PrivateKey:     cfg.DKIMPrivateKey,
		PrivateKeyFile: cfg.DKIMPrivateKeyFile,
	})
	if err != nil {
		return nil, err
	}
	if cfg.Mode == "relay" {
		return mailinfra.NewSMTPDelivery(mailinfra.SMTPConfig{
			Addr:     cfg.Addr,
			Username: cfg.Username,
			Password: cfg.Password,
			From:     cfg.From,
			DKIM:     dkimSigner,
		}), nil
	}
	return mailinfra.NewDirectSMTPDelivery(mailinfra.DirectSMTPConfig{
		From:       cfg.From,
		Domain:     cfg.Domain,
		HELODomain: cfg.HELODomain,
		DKIM:       dkimSigner,
	}), nil
}

func outboundSender(cfg platform.SMTPConfig) string {
	if strings.TrimSpace(cfg.From) != "" {
		return strings.TrimSpace(cfg.From)
	}
	return strings.TrimSpace(cfg.Username)
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
