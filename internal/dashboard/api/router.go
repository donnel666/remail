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
