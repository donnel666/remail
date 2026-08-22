package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker, turnstileGuard gin.HandlerFunc) {
	handler := NewHandler(module)

	public := rg.Group("")
	public.Use(middleware.LoadSession(fetcher))
	public.GET("/lotteries/:token", handler.GetPublicLottery)

	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	if turnstileGuard != nil {
		auth.Use(turnstileGuard)
	}
	auth.POST("/lotteries/:token/entries", handler.PostPublicLotteryEntry)

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	admin.GET("/lotteries", middleware.PermissionRequired(checker, "lottery:lottery", "read"), handler.GetAdminLotteries)
	admin.POST("/lotteries", middleware.PermissionRequired(checker, "lottery:lottery", "write"), handler.PostAdminLottery)
	admin.GET("/lotteries/:lotteryId", middleware.PermissionRequired(checker, "lottery:lottery", "read"), handler.GetAdminLottery)
	admin.GET("/lotteries/:lotteryId/entries", middleware.PermissionRequired(checker, "lottery:lottery", "read"), handler.GetAdminLotteryEntries)
	admin.GET("/lotteries/:lotteryId/payouts", middleware.PermissionRequired(checker, "lottery:lottery", "read"), handler.GetAdminLotteryPayouts)
}
