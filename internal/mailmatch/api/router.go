package api

import (
	"github.com/donnel666/remail/api/middleware"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, mod *Module) {
	h := NewHandler(mod)

	rg.GET("/pickup", h.GetPickupMessages)
	rg.POST("/pickup/batch", h.PostPickupMessagesBatch)
	rg.GET("/pickup/messages/:messageId", h.GetPickupMessage)
}

// RegisterBotRoutes expects the caller to install bot-key, channel, subject and
// scene middleware before this handler. The resolver must only return a user ID
// derived from the authenticated bot subject, never from the body.
func RegisterBotRoutes(rg *gin.RouterGroup, mod *Module, resolveUserID BotUserIDResolver) {
	var service *mailmatchapp.BotDiagnosisService
	if mod != nil {
		service = mod.BotDiagnosis
	}
	h := botDiagnosisHandler{service: service, userID: resolveUserID}
	rg.POST("/diagnoses/code", h.PostCodeDiagnosis)
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
	admin.POST(
		"/resources/:resourceId/projects/scan",
		middleware.PermissionRequired(checker, "core:resource", "operate"),
		h.PostAdminMicrosoftResourceProjectScan,
	)
}
