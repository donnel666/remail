package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterBillingRoutes(rg *gin.RouterGroup, mod *BillingModule, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewBillingHandler(mod, checker)

	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		auth.GET("/wallet", h.GetWallet)
		auth.GET("/wallet/referrals", h.GetWalletReferrals)
		auth.POST("/wallet/referrals/transfer", h.PostWalletReferralTransfer)
		auth.GET("/wallet/transactions", h.GetWalletTransactions)
		auth.GET("/recharges", h.GetRecharges)
		auth.POST("/cards/redeem", h.PostCardRedeem)
	}

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.POST("/wallets/:userId/credit", middleware.PermissionRequired(checker, "billing:wallet", "write"), h.PostAdminWalletCredit)
		admin.POST("/wallets/:userId/debit", middleware.PermissionRequired(checker, "billing:wallet", "write"), h.PostAdminWalletDebit)
		admin.GET("/cards", middleware.PermissionRequired(checker, "billing:card", "read"), h.GetAdminCards)
		admin.POST("/cards", middleware.PermissionRequired(checker, "billing:card", "write"), h.PostAdminCards)
		admin.PATCH("/cards/:cardKey", middleware.PermissionRequired(checker, "billing:card", "operate"), h.PatchAdminCard)
	}
}
