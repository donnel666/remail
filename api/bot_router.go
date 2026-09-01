package api

import (
	"log/slog"
	"net/http"

	"github.com/donnel666/remail/api/middleware"
	billingapi "github.com/donnel666/remail/internal/billing/api"
	coreapi "github.com/donnel666/remail/internal/core/api"
	dashboardapi "github.com/donnel666/remail/internal/dashboard/api"
	iamapi "github.com/donnel666/remail/internal/iam/api"
	mailmatchapi "github.com/donnel666/remail/internal/mailmatch/api"
	settingsapp "github.com/donnel666/remail/internal/systemsettings/app"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	botIntegrationRequestsPerMinute = 1200
	botSubjectReadsPerMinute        = 60
	botDiagnosesPerMinute           = 10
)

func registerBotRoutes(
	v1 *gin.RouterGroup,
	dispatch http.Handler,
	systemKeys middleware.BotSystemKeyAuthenticator,
	iamMod *iamapi.IAMModule,
	coreMod *coreapi.CoreModule,
	mailmatchMod *mailmatchapi.Module,
	dashboardMod *dashboardapi.Module,
	billingMod *billingapi.BillingModule,
	rdb redis.UniversalClient,
	eventSources ...botWebSocketEventSource,
) {
	bot := v1.Group("/bot")
	bot.Use(middleware.RateLimitPerClientIP(rdb, "bot_auth", 600, 60))
	bot.Use(middleware.BotSystemKeyRequired(systemKeys))
	bot.Use(middleware.BotChannelRequired())
	bot.Use(middleware.RateLimitPerSystemKey(rdb, "bot_all", botIntegrationRequestsPerMinute, 60))
	bot.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	contextReads := bot.Group("")
	contextReads.Use(middleware.BotIdentityRequired())
	contextReads.Use(middleware.RateLimitPerBotSubject(rdb, "context_read", botSubjectReadsPerMinute, 60))
	contextReads.GET("/context", getBotContext)

	iamapi.RegisterBotBindingRoutes(bot, iamMod, rdb)
	resolveUser := botUserResolver(iamMod)
	coreapi.RegisterBotProjectRoutes(
		bot, coreMod, resolveUser,
		middleware.BotIdentityRequired(),
		middleware.RateLimitPerBotSubject(rdb, "project_read", botSubjectReadsPerMinute, 60),
	)

	diagnostic := bot.Group("")
	diagnostic.Use(middleware.BotIdentityRequired())
	diagnostic.Use(middleware.RateLimitPerBotSubject(rdb, "code_diagnosis", botDiagnosesPerMinute, 60))
	mailmatchapi.RegisterBotRoutes(diagnostic, mailmatchMod, botDiagnosisUserResolver(iamMod))

	rankings := bot.Group("")
	rankings.Use(middleware.BotIdentityRequired())
	rankings.Use(middleware.RateLimitPerBotSubject(rdb, "ranking_read", botSubjectReadsPerMinute, 60))
	dashboardapi.RegisterBotRoutes(rankings, dashboardMod)
	billingapi.RegisterBotRoutes(rankings, billingMod)

	registerBotWebSocketRoute(bot, dispatch, systemKeys, eventSources...)
}

func botDiagnosisUserResolver(iamMod *iamapi.IAMModule) mailmatchapi.BotUserIDResolver {
	return func(c *gin.Context) (uint, bool) {
		identity, ok := middleware.GetCurrentBotIdentity(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required."})
			return 0, false
		}
		if iamMod == nil || iamMod.BotBindingUseCase == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable."})
			return 0, false
		}
		userID, found, err := iamMod.BotBindingUseCase.ResolveActiveUserID(
			c.Request.Context(), identity.Platform, identity.SubjectNamespace, identity.Subject,
		)
		if err != nil {
			slog.Error("bot diagnosis identity resolution failed", "request_id", middleware.GetRequestID(c), "error", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "Service is temporarily unavailable."})
			return 0, false
		}
		if !found {
			return 0, true
		}
		return userID, true
	}
}

func getBotContext(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"authorized": true})
}

func botUserResolver(iamMod *iamapi.IAMModule) coreapi.BotProjectUserResolver {
	return func(c *gin.Context) (coreapi.BotProjectViewer, bool) {
		identity, ok := middleware.GetCurrentBotIdentity(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"message": "Authentication is required.", "requestId": middleware.GetRequestID(c),
			})
			return coreapi.BotProjectViewer{}, false
		}
		if identity.Scene != middleware.BotScenePrivate {
			return coreapi.BotProjectViewer{PriceDiscountRatio: "1"}, true
		}
		if iamMod == nil || iamMod.BotBindingUseCase == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"message": "Service is temporarily unavailable.", "requestId": middleware.GetRequestID(c),
			})
			return coreapi.BotProjectViewer{}, false
		}
		resolved, found, err := iamMod.BotBindingUseCase.ResolveActiveUser(
			c.Request.Context(), identity.Platform, identity.SubjectNamespace, identity.Subject,
		)
		if err != nil {
			slog.Error("bot identity resolution failed", "request_id", middleware.GetRequestID(c), "error", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"message": "Service is temporarily unavailable.", "requestId": middleware.GetRequestID(c),
			})
			return coreapi.BotProjectViewer{}, false
		}
		if !found {
			return coreapi.BotProjectViewer{PriceDiscountRatio: "1"}, true
		}
		return coreapi.BotProjectViewer{
			UserID: resolved.UserID, PriceDiscountRatio: resolved.PriceDiscountRatio,
		}, true
	}
}

var _ middleware.BotSystemKeyAuthenticator = (*settingsapp.SystemKeyUseCase)(nil)
