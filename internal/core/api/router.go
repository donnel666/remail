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
		auth.GET("/resources/imports/:importId", h.GetResourceImport)
		auth.POST("/resources/delete", h.PostResourceDeleteBatch)
		auth.POST("/resources/publish", h.PostResourcePublishBatch)
		auth.POST("/resources/:resourceId/publish", h.PostResourcePublish)
		auth.POST("/resources/:resourceId/validate", h.PostResourceValidate)
		auth.POST("/resources/validations", h.PostResourceValidations)
		auth.GET("/resources/validations/:validationId", h.GetResourceValidation)

		// Project square and user project applications
		auth.GET("/projects", h.GetProjects)
		auth.POST("/projects", h.PostProject)
		auth.GET("/projects/:projectId", h.GetProject)
		auth.POST("/projects/:projectId/resubmit", h.PostProjectResubmit)
		auth.GET("/projects/logos/:logoKey", h.GetProjectLogo)

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
		admin.GET("/resources", middleware.PermissionRequired(checker, "core:resource", "read"), h.GetAdminMicrosoftResources)
		admin.POST("/resources/imports", middleware.PermissionRequired(checker, "core:resource", "write"), h.PostAdminMicrosoftResourceImport)
		admin.GET("/resources/imports/:importId", middleware.PermissionRequired(checker, "core:resource", "read"), h.GetAdminMicrosoftResourceImport)
		admin.POST("/resources/validations", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourceValidations)
		admin.POST("/resources/disable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourcesDisable)
		admin.POST("/resources/publish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourcesPublish)
		admin.POST("/resources/unpublish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourcesUnpublish)
		admin.POST("/resources/delete", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourcesDelete)
		admin.GET("/resources/:resourceId", middleware.PermissionRequired(checker, "core:resource", "read"), h.GetAdminMicrosoftResource)
		admin.GET("/resources/:resourceId/aliases", middleware.PermissionRequired(checker, "core:resource", "read"), h.GetAdminMicrosoftResourceAliases)
		admin.PATCH("/resources/:resourceId", middleware.PermissionRequired(checker, "core:resource", "write"), h.PatchAdminMicrosoftResource)
		admin.PUT("/resources/:resourceId/credentials", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PutAdminMicrosoftResourceCredentials)
		admin.POST("/resources/:resourceId/validate", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourceValidate)
		admin.POST("/resources/:resourceId/enable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourceEnable)
		admin.POST("/resources/:resourceId/disable", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourceDisable)
		admin.POST("/resources/:resourceId/publish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourcePublish)
		admin.POST("/resources/:resourceId/unpublish", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourceUnpublish)
		admin.DELETE("/resources/:resourceId", middleware.PermissionRequired(checker, "core:resource", "operate"), h.DeleteAdminMicrosoftResource)
		admin.POST("/resources/:resourceId/recover", middleware.PermissionRequired(checker, "core:resource", "operate"), h.PostAdminMicrosoftResourceRecover)

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
		admin.POST("/projects/logos", middleware.PermissionRequired(checker, "core:project", "write"), h.PostAdminProjectLogo)
	}
}
