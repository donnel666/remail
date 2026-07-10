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
		auth.GET("/projects/:projectId/inventory", h.GetUserProjectInventory)
	}

	admin := rg.Group("/admin")
	admin.Use(middleware.LoadSession(fetcher))
	admin.Use(middleware.AuthRequired())
	admin.Use(middleware.CSRFRequired())
	{
		admin.GET("/allocations", middleware.PermissionRequired(checker, "alloc:allocation", "read"), h.GetAllocations)
		admin.GET("/allocations/:allocationId", middleware.PermissionRequired(checker, "alloc:allocation", "read"), h.GetAllocation)
		admin.GET("/orders/:orderNo/allocations", middleware.PermissionRequired(checker, "alloc:allocation", "read"), h.GetOrderAllocation)
		admin.GET("/projects/:projectId/inventory", middleware.PermissionRequired(checker, "alloc:allocation", "read"), h.GetProjectInventory)
	}
}
