package api

import (
	"github.com/donnel666/remail/api/middleware"
	billingapi "github.com/donnel666/remail/internal/billing/api"
	coreapi "github.com/donnel666/remail/internal/core/api"
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	tradeapi "github.com/donnel666/remail/internal/trade/api"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// openCardRedeemPerMinute bounds card-key redemption on the API-key surface.
// The console route is protected by a Turnstile challenge, which an API-key
// client cannot solve, so brute force would otherwise just move here.
const openCardRedeemPerMinute = 1

func registerOpenRoutes(
	v1 *gin.RouterGroup,
	openapiMod *openapiapi.Module,
	coreMod *coreapi.CoreModule,
	billingMod *billingapi.BillingModule,
	tradeMod *tradeapi.Module,
	checker middleware.PermissionChecker,
	rdb redis.UniversalClient,
) {
	open := v1.Group("/open")
	open.Use(openapiapi.LoadAPIKey(openapiMod.UseCase))
	open.Use(openapiapi.KeyRequired())

	openHandler := openapiapi.NewHandler(openapiMod)
	coreHandler := coreapi.NewOpenCoreHandler(coreMod, checker)
	billingHandler := billingapi.NewBillingHandler(billingMod, checker)
	tradeHandler := tradeapi.NewHandler(tradeMod)

	open.GET("/apikey/profile", openHandler.GetAPIKeyProfile)

	open.GET("/projects", coreHandler.GetProjects)
	open.GET("/projects/:projectId", coreHandler.GetProject)

	open.POST("/orders", tradeHandler.PostOrder)
	open.POST("/orders/batch", tradeHandler.PostOrderBatch)
	open.GET("/orders", tradeHandler.GetOrders)
	open.GET("/orders/:orderNo", tradeHandler.GetOrder)

	open.GET("/wallet", billingHandler.GetWallet)
	open.GET("/wallet/transactions", billingHandler.GetWalletTransactions)
	open.GET("/recharges", billingHandler.GetRecharges)
	open.POST(
		"/cards/redeem",
		middleware.RateLimitPerUser(rdb, "open_card_redeem", openCardRedeemPerMinute, 60),
		billingHandler.PostCardRedeem,
	)

	open.GET("/resources", coreHandler.GetResources)
	open.GET("/resources/:resourceId", coreHandler.GetResourceDetail)
	open.DELETE("/resources/:resourceId", coreHandler.DeleteResource)
	open.POST("/resources/:resourceId/validate", coreHandler.PostResourceValidate)
	open.POST("/resources/imports", coreHandler.PostResourceImport)
	open.GET("/resources/imports/:importId", coreHandler.GetResourceImport)
	open.POST("/resources/validations", coreHandler.PostResourceValidations)
	open.GET("/servers", coreHandler.GetServers)
	open.POST("/servers", coreHandler.PostServer)
	open.POST("/domains", coreHandler.PostDomain)
	open.GET("/domains/:domainId/mailboxes", coreHandler.GetDomainMailboxes)
}
