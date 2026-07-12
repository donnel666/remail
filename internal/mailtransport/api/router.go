package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterMailTransportRoutes(rg *gin.RouterGroup, mod *MailTransportModule, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	handler := NewMailTransportHandler(mod)
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/bindings", middleware.PermissionRequired(checker, "mailtransport:binding", "read"), handler.GetAdminBindings)
		admin.GET("/bindings/messages/:messageId", middleware.PermissionRequired(checker, "mailtransport:binding", "read"), handler.GetAdminBindingMessage)
		admin.POST("/resources/:resourceId/aliases", middleware.PermissionRequired(checker, "core:resource", "operate"), handler.PostAdminMicrosoftResourceAlias)
		admin.POST("/resources/:resourceId/token/refresh", middleware.PermissionRequired(checker, "core:resource", "operate"), handler.PostAdminMicrosoftResourceTokenRefresh)
	}
}
