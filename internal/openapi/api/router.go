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
		auth.POST("/apikeys", h.PostAPIKey)
		auth.GET("/apikeys", h.GetAPIKeys)
		auth.GET("/apikeys/usage", h.GetAPIKeyUsage)
		auth.GET("/apikeys/:keyId", h.GetAPIKey)
		auth.PATCH("/apikeys/:keyId", h.PatchAPIKey)
		auth.DELETE("/apikeys/:keyId", h.DeleteAPIKey)
	}

	// Admin per-user API key management, authorized by iam:user:operate.
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/users/:userId/apikeys", middleware.PermissionRequired(checker, "iam:user", "operate"), h.GetAdminUserAPIKeys)
		admin.POST("/users/:userId/apikeys", middleware.PermissionRequired(checker, "iam:user", "operate"), h.PostAdminUserAPIKey)
		admin.PATCH("/users/:userId/apikeys/:keyId", middleware.PermissionRequired(checker, "iam:user", "operate"), h.PatchAdminUserAPIKey)
		admin.DELETE("/users/:userId/apikeys/:keyId", middleware.PermissionRequired(checker, "iam:user", "operate"), h.DeleteAdminUserAPIKey)
	}
}
