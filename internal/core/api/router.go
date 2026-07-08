package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterCoreRoutes registers all Core (resource) routes on the given router group.
// P1-I2: supplier resource upload, list, detail. Admin routes come in later iterations.
// The fetcher is used by LoadSession middleware to authenticate users.
func RegisterCoreRoutes(rg *gin.RouterGroup, mod *CoreModule, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewCoreHandler(mod, checker)

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
		auth.POST("/resource-validations", h.PostResourceValidations)
		auth.GET("/resource-validations/:validationId", h.GetResourceValidation)

		// Project square and user project applications
		auth.GET("/projects", h.GetProjects)
		auth.POST("/projects", h.PostProject)
		auth.GET("/projects/:projectId", h.GetProject)
		auth.POST("/projects/:projectId/resubmit", h.PostProjectResubmit)
		auth.GET("/project-logos/:logoKey", h.GetProjectLogo)

		// Mail server management
		auth.GET("/servers", h.GetServers)
		auth.POST("/servers", h.PostServer)

		// Domain resource management
		auth.POST("/domains", h.PostDomain)
		auth.GET("/domains/:domainId/mailboxes", h.GetDomainMailboxes)
	}

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.POST("/projects", middleware.PermissionRequired(checker, "core:project", "write"), h.PostAdminProject)
		admin.POST("/projects/relist", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectsRelist)
		admin.POST("/projects/delist", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectsDelist)
		admin.POST("/projects/delete", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectsDelete)
		admin.PUT("/projects/:projectId", middleware.PermissionRequired(checker, "core:project", "write"), h.PutAdminProject)
		admin.POST("/projects/:projectId/approve", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectApprove)
		admin.POST("/projects/:projectId/reject", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectReject)
		admin.POST("/projects/:projectId/duplicate", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectDuplicate)
		admin.POST("/projects/:projectId/relist", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectRelist)
		admin.POST("/projects/:projectId/delist", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectDelist)
		admin.DELETE("/projects/:projectId", middleware.PermissionRequired(checker, "core:project", "operate"), h.DeleteAdminProject)
		admin.GET("/projects/:projectId/access", middleware.PermissionRequired(checker, "core:project", "read"), h.GetAdminProjectAccess)
		admin.POST("/projects/:projectId/access", middleware.PermissionRequired(checker, "core:project", "operate"), h.PostAdminProjectAccess)
		admin.DELETE("/projects/:projectId/access/:userId", middleware.PermissionRequired(checker, "core:project", "operate"), h.DeleteAdminProjectAccess)
		admin.POST("/project-logos", middleware.PermissionRequired(checker, "core:project", "write"), h.PostAdminProjectLogo)
	}
}
