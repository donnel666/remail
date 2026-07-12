package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, module *Module, fetcher middleware.SessionFetcher, checker middleware.PermissionChecker) {
	handler := NewHandler(module)
	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	admin.GET("/tasks", middleware.PermissionRequired(checker, "governance:task", "read"), handler.GetAdminTasks)
	admin.GET("/tasks/:taskId", middleware.PermissionRequired(checker, "governance:task", "read"), handler.GetAdminTask)
}
