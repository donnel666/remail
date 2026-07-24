package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts administrator system-settings CRUD endpoints under
// /admin/settings. Values intentionally remain strings so the same endpoint
// can carry scalars, JSON documents, or Markdown/HTML.
func RegisterRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewHandler(module)
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/settings", middleware.PermissionRequired(checker, "system:settings", "read"), h.Get)
		admin.GET("/settings/:key", middleware.PermissionRequired(checker, "system:settings", "read"), h.GetOne)
		admin.PUT("/settings", middleware.PermissionRequired(checker, "system:settings", "write"), h.PutBulk)
		admin.PUT("/settings/:key", middleware.PermissionRequired(checker, "system:settings", "write"), h.Put)
		admin.DELETE("/settings/:key", middleware.PermissionRequired(checker, "system:settings", "write"), h.Delete)
	}
}
