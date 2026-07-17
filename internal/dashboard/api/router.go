package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the console dashboard under the standard authenticated
// user group (session + auth + CSRF). No permission check: any signed-in user
// sees their own dashboard. CSRFRequired is a no-op on GET but kept to mirror
// the house route group.
func RegisterRoutes(rg *gin.RouterGroup, mod *Module, fetcher middleware.SessionFetcher) {
	h := NewHandler(mod)

	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		auth.GET("/dashboard", h.GetDashboard)
	}
}

// RegisterAdminRoutes mounts the platform dashboard under /admin, gated by the
// same permission as the finance summary it draws from.
func RegisterAdminRoutes(rg *gin.RouterGroup, mod *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewHandler(mod)

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/dashboard", middleware.PermissionRequired(checker, "billing:wallet", "read"), h.GetAdminDashboard)
	}
}
