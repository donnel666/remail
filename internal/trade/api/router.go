package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, mod *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewHandler(mod)

	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		auth.GET("/orders", h.GetOrders)
		auth.GET("/orders/:orderNo", h.GetOrder)
		auth.GET("/orders/:orderNo/events", h.GetOrderEvents)
		auth.POST("/orders", h.PostOrder)
		auth.POST("/orders/batch", h.PostOrderBatch)
		auth.POST("/orders/:orderNo/archive", h.PostOrderArchive)
	}

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.POST("/orders/:orderNo/refund", middleware.PermissionRequired(checker, "trade:order", "operate"), h.PostAdminOrderRefund)
		admin.POST("/orders/:orderNo/refund/retry", middleware.PermissionRequired(checker, "trade:order", "operate"), h.PostAdminOrderRefundRetry)
		admin.POST("/orders/:orderNo/terminate", middleware.PermissionRequired(checker, "trade:order", "operate"), h.PostAdminOrderTerminate)
		admin.POST("/orders/:orderNo/cleanup/retry", middleware.PermissionRequired(checker, "trade:order", "operate"), h.PostAdminOrderCleanupRetry)
		admin.POST("/orders/timeouts/scan", middleware.PermissionRequired(checker, "trade:order", "operate"), h.PostAdminOrderTimeoutScan)
	}
}
