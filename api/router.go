package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/donnel666/remail/api/health"
	"github.com/donnel666/remail/api/middleware"
	allocapi "github.com/donnel666/remail/internal/alloc/api"
	billingapi "github.com/donnel666/remail/internal/billing/api"
	coreapi "github.com/donnel666/remail/internal/core/api"
	governanceapi "github.com/donnel666/remail/internal/governance/api"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	iamapi "github.com/donnel666/remail/internal/iam/api"
	mailmatchapi "github.com/donnel666/remail/internal/mailmatch/api"
	mailapi "github.com/donnel666/remail/internal/mailtransport/api"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	"github.com/donnel666/remail/internal/platform"
	proxyapi "github.com/donnel666/remail/internal/proxy/api"
	tradeapi "github.com/donnel666/remail/internal/trade/api"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
)

// SetupRouter creates the Gin engine with all middleware and route registrations.
// feFS is the embedded frontend dist filesystem (nil in development mode).
func SetupRouter(p *platform.Platform, feFS fs.FS) (*gin.Engine, func(context.Context), error) {
	r := gin.New()
	cleanupFuncs := make([]func(context.Context), 0, 4)
	cleanup := func(ctx context.Context) {
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			cleanupFuncs[i](ctx)
		}
	}

	// Global middleware
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(platform.HTTPMetricsMiddleware())
	r.Use(middleware.RequestLogger(p.Diagnostics.SlowRequestThreshold))
	r.Use(middleware.CORS("http://localhost:3000", "http://127.0.0.1:3000"))

	// Health check endpoints (outside /v1)
	h := health.NewHandler(p)
	r.GET("/healthz", h.Healthz)
	r.GET("/readyz", h.Readyz)
	if sqlDB, err := p.DB.DB(); err == nil {
		platform.SetMetricsDB(sqlDB)
	}
	r.GET("/metrics", gin.WrapH(platform.MetricsHandler()))

	// API v1 routes
	taskMux := asynq.NewServeMux()
	var mailMod *mailapi.MailTransportModule
	var coreMod *coreapi.CoreModule
	v1 := r.Group("/v1")
	{
		// IAM module (activation, auth, users)
		fileStore := governanceinfra.NewMinIOFileStore(p.MinIO, p.MinIOBucket)
		retentionLocation, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			retentionLocation = time.FixedZone("Asia/Shanghai", 8*60*60)
		}
		retentionService := governanceapp.NewRetentionService(
			governanceinfra.NewRetentionRepo(p.DB),
			fileStore,
			governanceinfra.NewSystemLogRepo(p.DB),
		)
		cleanupFuncs = append(cleanupFuncs, retentionService.StartDaily(context.Background(), retentionLocation))

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
		mailMod.SetBackgroundExecutionGate(p.BackgroundLoad)
		mailapi.RegisterMailTransportTaskHandlers(taskMux, mailMod)

		iamMod, err := iamapi.NewIAMModule(p.DB, p.Redis, mailMod.DeliveryUseCase)
		if err != nil {
			return nil, cleanup, err
		}
		iamapi.RegisterIAMRoutes(v1, iamMod, p.SessionMaxAge, p.SessionSecure)

		// Core module (resources, mail servers, domains)
		coreMod, err = coreapi.NewCoreModule(p.DB, p.Redis, fileStore, p.Asynq, mailMod.ValidationUseCase, mailMod.BindingRecorder)
		if err != nil {
			return nil, cleanup, err
		}
		mailMod.SetMicrosoftCredentialPort(coreMod.MicrosoftCredentials)
		coreMod.SetMicrosoftValidationBindingCommitPort(mailMod.ValidationBinding)
		coreMod.SetBackgroundExecutionGate(p.BackgroundLoad)
		coreMod.SetAdminProxyBindingQueryPort(proxyapi.NewAdminResourceProxyBindingQueryAdapter(proxyMod.AdminResourceBindings))
		coreMod.SetMicrosoftAliasScheduleTrigger(mailapi.NewMicrosoftAliasValidationAdapter(mailMod))
		coreapi.RegisterCoreTaskHandlers(taskMux, coreMod)
		iamSessionFetcher := iamapi.NewSessionFetcher(iamMod.SessionStore, iamMod.UserRepo)
		governanceMod := governanceapi.NewModule(p.DB)
		governanceapi.RegisterRoutes(v1, governanceMod, iamSessionFetcher, iamMod.PermissionChecker)
		mailapi.RegisterMailTransportRoutes(v1, mailMod, iamSessionFetcher, iamMod.PermissionChecker)

		// Allocation module (admin diagnostics and Trade-facing application port)
		allocMod := allocapi.NewModule(p.DB, p.Asynq)
		allocMod.UseCase.SetHistoricalMicrosoftAliasPort(mailMod.MicrosoftAliases)
		coreMod.SetAdminResourcePorts(
			iamMod.AdminResourceOwners,
			mailMod.BindingQuery,
			mailMod.BindingAdmin,
			allocMod.ResourceGuard,
			governanceMod.CoreTaskQuery,
			mailMod.AliasScheduleQuery,
		)
		coreapi.RegisterCoreRoutes(v1, coreMod, iamSessionFetcher, iamMod.PermissionChecker)
		allocapi.RegisterRoutes(v1, allocMod, iamSessionFetcher, iamMod.PermissionChecker)

		// Billing module (wallet, recharge ledger and card-key redemption)
		billingMod := billingapi.NewBillingModule(p.DB)
		billingMod.SetUserSelectionResolver(iamMod.AdminUserSelectionResolver)
		billingMod.SetUserDirectory(financeUserDirectory{users: iamMod.Users})
		billingapi.RegisterBillingRoutes(v1, billingMod, iamSessionFetcher, iamMod.PermissionChecker)

		// OpenAPI credentials and order service tokens.
		openapiMod := openapiapi.NewModule(p.DB)
		cleanupFuncs = append(cleanupFuncs, func(ctx context.Context) {
			if err := openapiMod.UseCase.Close(ctx); err != nil {
				slog.Error("failed to flush OpenAPI runtime state during shutdown", "error", err)
			}
		})
		openapiapi.RegisterRoutes(v1, openapiMod, iamSessionFetcher, iamMod.PermissionChecker)

		// Trade module (unified console/API Key checkout and order query).
		tradeMod := tradeapi.NewModule(p.DB, coreMod.ProjectUseCase, billingMod.WalletUseCase, allocMod.UseCase, openapiMod.UseCase)
		tradeapi.RegisterRoutes(v1, tradeMod, iamSessionFetcher, iamMod.PermissionChecker)
		cleanupFuncs = append(cleanupFuncs, tradeapi.StartLifecycleScanner(tradeMod))

		// MailMatch module (order-scoped message cache, async fetch and matching).
		mailmatchMod := mailmatchapi.NewModule(p.DB, fileStore, p.Asynq, proxyMod.ProxyUseCase, tradeMod.UseCase)
		mailmatchMod.SetMicrosoftCredentialPort(coreMod.MicrosoftCredentials)
		mailmatchMod.SetBackgroundExecutionGate(p.BackgroundLoad)
		coreMod.SetAdminResourceMaintenancePort(adminMicrosoftMaintenanceAdapter{
			aliases: mailMod.MicrosoftAliases,
			tokens:  mailMod.TokenRefresh,
			history: mailmatchMod.ResourceFetch,
		})
		coreMod.ProjectUseCase.SetHistoryScan(mailmatchMod.ProjectHistory.Schedule)
		coreMod.SetMicrosoftHistoryScanTrigger(mailmatchMod.ProjectHistory)
		mailMod.SetInboundConsumer(mailmatchapi.NewInboundConsumerAdapter(mailmatchMod.UseCase))
		mailmatchapi.RegisterTaskHandlers(taskMux, mailmatchMod)
		mailmatchapi.RegisterRoutes(v1, mailmatchMod)
		mailmatchapi.RegisterAdminRoutes(v1, mailmatchMod, iamSessionFetcher, iamMod.PermissionChecker)

		registerOpenRoutes(v1, openapiMod, coreMod, billingMod, tradeMod, iamMod.PermissionChecker)

		// Proxy module (admin proxy pool maintenance)
		proxyapi.RegisterProxyTaskHandlers(taskMux, proxyMod)
		proxyapi.RegisterProxyRoutes(v1, proxyMod, iamSessionFetcher, iamMod.PermissionChecker)
	}
	if err := p.RealtimeAsynqServer.Start(taskMux); err != nil {
		cleanup(context.Background())
		return nil, cleanup, err
	}
	if err := p.AsynqServer.Start(taskMux); err != nil {
		p.ShutdownWorkers()
		cleanup(context.Background())
		return nil, cleanup, err
	}
	if p.BackgroundLoad != nil {
		cleanupFuncs = append(cleanupFuncs, p.BackgroundLoad.Start(context.Background()))
	}
	if err := p.BackgroundAsynqServer.Start(taskMux); err != nil {
		p.ShutdownWorkers()
		cleanup(context.Background())
		return nil, cleanup, err
	}
	p.MarkWorkersReady()
	if coreMod != nil {
		cleanupFuncs = append(cleanupFuncs, coreapi.StartResourceValidationDispatcher(context.Background(), coreMod))
	}
	if mailMod != nil {
		cleanupFuncs = append(cleanupFuncs, mailMod.Start(context.Background()))
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
