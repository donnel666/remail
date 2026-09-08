package api

import (
	"context"

	"github.com/donnel666/remail/api/middleware"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Module owns Proto HTTP dependencies. It deliberately does not accept a
// Microsoft service or any Microsoft-specific port.
type Module struct {
	Service               *infra.Service
	Queue                 app.Queue
	Files                 governanceapp.FilePort
	OperationLogs         governanceapp.OperationLogPort
	SystemLogs            governanceapp.SystemLogPort
	ValidateOwner         func(context.Context, uint) (bool, error)
	ValidateSupplierOwner func(context.Context, uint) (bool, error)
	OwnerSummaries        func(context.Context, []uint) (map[uint]OwnerSummary, error)
	SearchOwners          func(context.Context, string, int) ([]uint, error)
}

func NewModule(db *gorm.DB, queue *asynq.Client, files ...governanceapp.FilePort) *Module {
	var fileStore governanceapp.FilePort
	if len(files) > 0 {
		fileStore = files[0]
	}
	service := infra.NewService(db, fileStore)
	service.Queue = queue
	return &Module{Service: service, Queue: queue, Files: fileStore}
}

// NewModuleWithQueue is useful for tests and for deployments that wrap Asynq
// behind a narrow queue adapter.
func NewModuleWithQueue(service *infra.Service, queue app.Queue) *Module {
	if service != nil {
		service.Queue = queue
	}
	return &Module{Service: service, Queue: queue}
}

func (m *Module) SetAuditLogs(operationLogs governanceapp.OperationLogPort, systemLogs governanceapp.SystemLogPort) {
	if m == nil {
		return
	}
	m.OperationLogs = operationLogs
	m.SystemLogs = systemLogs
	if m.Service != nil {
		m.Service.OperationLogs = operationLogs
		m.Service.SystemLogs = systemLogs
	}
}

func (m *Module) SetRedis(client redis.UniversalClient) {
	if m != nil && m.Service != nil {
		m.Service.Redis = client
	}
}
func (m *Module) SetBackgroundExecutionGate(gate infra.BackgroundExecutionGate) {
	if m != nil && m.Service != nil {
		m.Service.BackgroundExecution = gate
	}
}
func (m *Module) ScheduleProjectHistory(ctx context.Context, projectID uint, requestID string) error {
	return m.Service.ScheduleProjectHistory(ctx, projectID, requestID)
}

// RegisterRoutes installs only Proto paths. The root router decides when to
// call this function; no shared resource route is modified here.
func RegisterRoutes(
	rg *gin.RouterGroup,
	module *Module,
	fetcher middleware.SessionFetcher,
	checker middleware.PermissionChecker,
	guards ...gin.HandlerFunc,
) {
	if rg == nil {
		return
	}
	h := &handler{module: module, checker: checker}
	if module != nil && module.Service != nil {
		module.Service.ValidateOwner = module.ValidateOwner
		module.Service.ValidateSupplierOwner = module.ValidateSupplierOwner
	}
	auth := rg.Group("")
	auth.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	auth.Use(middleware.RoleRequired(iamdomain.RoleAdmin, iamdomain.RoleSuperAdmin))
	for _, guard := range guards {
		if guard != nil {
			auth.Use(guard)
		}
	}
	auth.GET("/proto/resources", permission(checker, "read"), h.listOwned)
	auth.POST("/proto/resources/imports", permission(checker, "write"), h.importOwned)
	auth.GET("/proto/resources/imports/:importId", permission(checker, "read"), h.getImportOwned)
	auth.GET("/proto/resources/imports/:importId/items", permission(checker, "read"), h.getImportItemsOwned)
	auth.GET("/proto/resources/imports/:importId/failures", permission(checker, "read"), h.getImportFailuresOwned)
	auth.POST("/proto/resources/validations", permission(checker, "operate"), h.validateOwnedBatch)
	auth.POST("/proto/resources/bulk/:command", permission(checker, "operate"), h.bulkOwned)
	auth.GET("/proto/resources/bulk-tasks/:taskId", permission(checker, "read"), h.getBulkOwned)
	auth.GET("/proto/resources/:resourceId", permission(checker, "read"), h.getOwned)
	auth.POST("/proto/resources/:resourceId/validate", permission(checker, "operate"), h.validateOwned)
	auth.POST("/proto/resources/:resourceId/publish", permission(checker, "operate"), h.publishOwned)
	auth.DELETE("/proto/resources/:resourceId", permission(checker, "operate"), h.deleteOwned)

	admin := rg.Group("/admin/proto/resources")
	admin.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	admin.Use(middleware.RoleRequired(iamdomain.RoleAdmin, iamdomain.RoleSuperAdmin))
	admin.GET("", permission(checker, "read"), h.listAdmin)
	admin.POST("/imports", permission(checker, "write"), h.importAdmin)
	admin.GET("/imports/:importId", permission(checker, "read"), h.getImportAdmin)
	admin.GET("/imports/:importId/items", permission(checker, "read"), h.getImportItemsAdmin)
	admin.GET("/imports/:importId/failures", permission(checker, "read"), h.getImportFailuresAdmin)
	admin.POST("/validations", permission(checker, "operate"), h.validateAdminBatch)
	admin.POST("/bulk/:command", permission(checker, "operate"), h.bulkAdmin)
	admin.GET("/bulk-tasks/:taskId", permission(checker, "read"), h.getBulkAdmin)
	admin.POST("/batch/validation", permission(checker, "operate"), h.batchCommand("validate"))
	admin.POST("/batch/history", permission(checker, "operate"), h.batchCommand("history"))
	admin.POST("/batch/disable", permission(checker, "operate"), h.batchCommand("disable"))
	admin.POST("/batch/publish", permission(checker, "operate"), h.batchCommand("publish"))
	admin.POST("/batch/unpublish", permission(checker, "operate"), h.batchCommand("unpublish"))
	admin.POST("/batch/delete", permission(checker, "operate"), h.batchCommand("delete"))
	admin.GET("/:resourceId", permission(checker, "read"), h.getAdmin)
	admin.GET("/:resourceId/maintenance", permission(checker, "read"), h.listMaintenanceAdmin)
	admin.PATCH("/:resourceId", permission(checker, "write"), h.patchAdmin)
	admin.PUT("/:resourceId/credentials", permission(checker, "operate"), h.replaceAdminCredentials)
	admin.POST("/:resourceId/validate", permission(checker, "operate"), h.validateAdmin)
	admin.POST("/:resourceId/history", permission(checker, "operate"), h.historyAdmin)
	admin.POST("/:resourceId/enable", permission(checker, "operate"), h.enableAdmin)
	admin.POST("/:resourceId/disable", permission(checker, "operate"), h.disableAdmin)
	admin.POST("/:resourceId/publish", permission(checker, "operate"), h.publishAdmin)
	admin.POST("/:resourceId/unpublish", permission(checker, "operate"), h.unpublishAdmin)
	admin.DELETE("/:resourceId", permission(checker, "operate"), h.deleteAdmin)
	admin.POST("/:resourceId/recover", permission(checker, "operate"), h.recoverAdmin)
}

func permission(checker middleware.PermissionChecker, action string) gin.HandlerFunc {
	return middleware.PermissionRequired(checker, "core:resource", action)
}

// RegisterOpenRoutes receives the existing API-key-authenticated group. Proto
// management is administrator-only; API keys retain their unprivileged effective
// role. Keep the guard on a child group so ordinary orders and wallet calls work.
func RegisterOpenRoutes(open *gin.RouterGroup, module *Module, checker middleware.PermissionChecker) {
	if open == nil || module == nil {
		return
	}
	h := &handler{module: module, checker: checker}
	resources := open.Group("", middleware.RoleRequired(iamdomain.RoleAdmin, iamdomain.RoleSuperAdmin))
	resources.GET("/proto/resources", permission(checker, "read"), h.listOwned)
	resources.GET("/proto/resources/:resourceId", permission(checker, "read"), h.getOwned)
	resources.POST("/proto/resources/imports", permission(checker, "write"), h.importOwned)
	resources.GET("/proto/resources/imports/:importId", permission(checker, "read"), h.getImportOwned)
	resources.GET("/proto/resources/imports/:importId/items", permission(checker, "read"), h.getImportItemsOwned)
	resources.GET("/proto/resources/imports/:importId/failures", permission(checker, "read"), h.getImportFailuresOwned)
	resources.POST("/proto/resources/:resourceId/validate", permission(checker, "operate"), h.validateOwned)
	resources.POST("/proto/resources/validations", permission(checker, "operate"), h.validateOwnedBatch)
	resources.POST("/proto/resources/bulk/:command", permission(checker, "operate"), func(c *gin.Context) {
		if c.Param("command") != "validate" && c.Param("command") != "delete" {
			writeProtoError(c, domain.ErrInvalidResource)
			return
		}
		h.bulkOwned(c)
	})
	resources.GET("/proto/resources/bulk-tasks/:taskId", permission(checker, "read"), h.getBulkOwned)
	resources.DELETE("/proto/resources/:resourceId", permission(checker, "operate"), h.deleteOwned)
}
