package api

import (
	"github.com/donnel666/remail/api/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, mod *Module, fetcher middleware.SessionFetcher) {
	h := NewHandler(mod)

	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(middleware.AuthRequired())
	auth.Use(middleware.CSRFRequired())
	{
		auth.POST("/apikeys", h.PostAPIKey)
		auth.GET("/apikeys", h.GetAPIKeys)
		auth.GET("/apikey-usage", h.GetAPIKeyUsage)
		auth.GET("/apikeys/:keyId", h.GetAPIKey)
		auth.PATCH("/apikeys/:keyId", h.PatchAPIKey)
		auth.DELETE("/apikeys/:keyId", h.DeleteAPIKey)
	}
}
