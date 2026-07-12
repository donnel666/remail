package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, mod *Module) {
	h := NewHandler(mod)

	rg.GET("/pickup", h.GetPickupMessages)
	rg.GET("/pickup/messages/:messageId", h.GetPickupMessage)
}

func RegisterAdminRoutes(rg *gin.RouterGroup, mod *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewHandler(mod)
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	admin.GET(
		"/messages",
		middleware.PermissionRequired(checker, "mailmatch:message", "read"),
		h.GetAdminMessages,
	)
	admin.GET(
		"/messages/:messageId",
		middleware.PermissionRequired(checker, "mailmatch:message", "read"),
		h.GetAdminMessage,
	)
	admin.POST(
		"/resources/:resourceId/messages/fetch",
		middleware.PermissionRequired(checker, "mailmatch:message", "operate"),
		h.PostAdminMicrosoftResourceMessagesFetch,
	)
}
