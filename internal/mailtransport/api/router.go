package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/gin-gonic/gin"
)

func RegisterMailTransportRoutes(rg *gin.RouterGroup, mod *MailTransportModule, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	handler := NewMailTransportHandler(mod)
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/bindings", auxiliaryMailReadPermission(checker), handler.GetAdminBindings)
		admin.GET("/bindings/messages/:messageId", auxiliaryMailReadPermission(checker), handler.GetAdminBindingMessage)
		admin.POST("/resources/:resourceId/aliases", middleware.PermissionRequired(checker, "core:resource", "operate"), handler.PostAdminMicrosoftResourceAlias)
		admin.POST("/resources/:resourceId/token/refresh", middleware.PermissionRequired(checker, "core:resource", "operate"), handler.PostAdminMicrosoftResourceTokenRefresh)
	}
}

func auxiliaryMailReadPermission(checker middleware.PermissionChecker) gin.HandlerFunc {
	bindingRead := middleware.PermissionRequired(checker, "mailtransport:binding", "read")
	messageRead := middleware.PermissionRequired(checker, "mailmatch:message", "read")
	return func(c *gin.Context) {
		resourceType, valid := parseAdminAuxiliaryResourceType(c)
		if valid && (resourceType == domain.InboundResourceDomain || resourceType == domain.InboundResourceICloud) {
			messageRead(c)
			return
		}
		bindingRead(c)
	}
}
