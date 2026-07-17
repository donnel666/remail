package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

// Permission pairs mirror the frontend gates: support actions are allowed to
// either user operators or order operators, while refunds require order operate.
var (
	ticketReadPermissions    = [][2]string{{"iam:user", "read"}, {"trade:order", "read"}}
	ticketOperatePermissions = [][2]string{{"iam:user", "operate"}, {"trade:order", "operate"}}
)

func RegisterRoutes(rg *gin.RouterGroup, mod *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	h := NewHandler(mod)

	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		auth.GET("/tickets", h.GetTickets)
		auth.GET("/tickets/:ticketNo", h.GetTicket)
		auth.GET("/tickets/:ticketNo/attachments/:attachmentNo", h.GetAttachment)
		auth.POST("/tickets", h.PostTicket)
		auth.POST("/tickets/:ticketNo/messages", h.PostTicketMessage)
		auth.POST("/tickets/:ticketNo/read", h.PostTicketRead)
		auth.POST("/tickets/:ticketNo/close", h.PostTicketClose)
	}

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/tickets", middleware.PermissionRequiredAny(checker, ticketReadPermissions...), h.GetAdminTickets)
		admin.POST("/tickets/:ticketNo/messages", middleware.PermissionRequiredAny(checker, ticketOperatePermissions...), h.PostAdminTicketMessage)
		admin.POST("/tickets/:ticketNo/read", middleware.PermissionRequiredAny(checker, ticketReadPermissions...), h.PostAdminTicketRead)
		admin.POST("/tickets/:ticketNo/close", middleware.PermissionRequiredAny(checker, ticketOperatePermissions...), h.PostAdminTicketClose)
		admin.POST("/tickets/:ticketNo/refund", middleware.PermissionRequired(checker, "trade:order", "operate"), h.PostAdminTicketRefund)
	}
}
