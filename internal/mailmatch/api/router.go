package api

import "github.com/gin-gonic/gin"

func RegisterRoutes(rg *gin.RouterGroup, mod *Module) {
	h := NewHandler(mod)

	rg.GET("/pickup", h.GetPickupMessages)
	rg.GET("/pickup/messages/:messageId", h.GetPickupMessage)
}
