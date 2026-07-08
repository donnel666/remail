package api

import (
	"github.com/donnel666/remail/api/middleware"
	openapiapi "github.com/donnel666/remail/internal/openapi/api"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(rg *gin.RouterGroup, mod *Module, fetcher middleware.SessionFetcher, openapiMod *openapiapi.Module) {
	h := NewHandler(mod)

	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher))
	auth.Use(openapiapi.LoadAPIKey(openapiMod.UseCase))
	auth.Use(openapiapi.MarkConsoleChannel())
	auth.Use(middleware.AuthRequired())
	{
		auth.GET("/orders", openapiapi.KeyAllowed(), openapiapi.RequirePrincipalAllowed(), h.GetOrders)
		auth.GET("/orders/:orderNo", openapiapi.KeyAllowed(), openapiapi.RequirePrincipalAllowed(), h.GetOrder)
		auth.GET("/orders/:orderNo/events", openapiapi.RequirePrincipalAllowed(), openapiapi.SessionOnly(), h.GetOrderEvents)
		auth.POST("/orders", openapiapi.KeyAllowed(), openapiapi.RequirePrincipalAllowed(), openapiapi.CSRFRequiredForSession(), h.PostOrder)
		auth.POST("/orders/:orderNo/archive", openapiapi.RequirePrincipalAllowed(), openapiapi.SessionOnly(), openapiapi.CSRFRequiredForSession(), h.PostOrderArchive)
	}
}
