package api

import (
	"github.com/donnel666/remail/api/middleware"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
)

func RegisterProxyRoutes(rg *gin.RouterGroup, mod *ProxyModule, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewProxyHandler(mod)

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	admin.Use(middleware.AdminRequired(iamdomain.RoleAdmin))
	{
		admin.GET("/proxies", middleware.PermissionRequired(checker, "proxy:proxy", "read"), h.GetProxies)
		admin.GET("/proxies/stats", middleware.PermissionRequired(checker, "proxy:proxy", "read"), h.GetProxyStats)
		admin.GET("/proxies/bindings", middleware.PermissionRequired(checker, "proxy:proxy", "read"), h.GetProxyBindings)
		admin.POST("/proxies/imports", middleware.PermissionRequired(checker, "proxy:proxy", "write"), h.PostProxyImports)
		admin.POST("/proxies/check", middleware.PermissionRequired(checker, "proxy:proxy", "operate"), h.PostProxyCheckBatch)
		admin.POST("/proxies/resource", middleware.PermissionRequired(checker, "proxy:proxy", "write"), h.PostResourceProxy)
		admin.POST("/proxies/system", middleware.PermissionRequired(checker, "proxy:proxy", "write"), h.PostSystemProxy)
		admin.POST("/proxies/delete", middleware.PermissionRequired(checker, "proxy:proxy", "write"), h.PostProxyDeleteBatch)
		admin.POST("/proxies/disable", middleware.PermissionRequired(checker, "proxy:proxy", "write"), h.PostProxyDisableBatch)
		admin.GET("/proxies/:proxyId", middleware.PermissionRequired(checker, "proxy:proxy", "read"), h.GetProxy)
		admin.PATCH("/proxies/:proxyId", middleware.PermissionRequired(checker, "proxy:proxy", "write"), h.PatchProxy)
		admin.POST("/proxies/:proxyId/check", middleware.PermissionRequired(checker, "proxy:proxy", "operate"), h.PostProxyCheck)
	}
}
