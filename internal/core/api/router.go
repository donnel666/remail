package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterCoreRoutes registers all Core (resource) routes on the given router group.
// P1-I2: supplier resource upload, list, detail. Admin routes come in later iterations.
// The fetcher is used by LoadSession middleware to authenticate users.
func RegisterCoreRoutes(rg *gin.RouterGroup, mod *CoreModule, fetcher middleware.SessionFetcher) {
	h := NewCoreHandler(mod)

	// ---- Authenticated routes (any role) ----
	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		// Resource management (supplier self-service)
		auth.GET("/resources", h.GetResources)
		auth.GET("/resources/:resourceId", h.GetResourceDetail)
		auth.DELETE("/resources/:resourceId", h.DeleteResource)
		auth.POST("/resources/imports", h.PostResourceImport)
		auth.GET("/resource-imports/:importId", h.GetResourceImport)
		auth.POST("/resources/delete", h.PostResourceDeleteBatch)
		auth.POST("/resources/publish", h.PostResourcePublishBatch)
		auth.POST("/resources/:resourceId/publish", h.PostResourcePublish)
		auth.POST("/resources/:resourceId/validate", h.PostResourceValidate)

		// Mail server management
		auth.GET("/servers", h.GetServers)
		auth.POST("/servers", h.PostServer)

		// Domain resource management
		auth.POST("/domains", h.PostDomain)
		auth.GET("/domains/:domainId/mailboxes", h.GetDomainMailboxes)
	}
}
