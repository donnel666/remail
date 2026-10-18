package api

import (
	"context"

	"github.com/donnel666/remail/api/middleware"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Module owns Daum HTTP dependencies and shared administrator diagnostics.
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
	ProxyBindings         coreapp.AdminProxyBindingQueryPort
	Tasks                 coreapp.TaskQueryPort
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

// RegisterRoutes installs only Daum paths. The root router decides when to
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
	owned := rg.Group("/daum/resources")
	owned.Use(middleware.LoadSession(fetcher), middleware.AuthRequired(), middleware.CSRFRequired())
	for _, guard := range guards {
		if guard != nil {
			owned.Use(guard)
		}
	}
	owned.GET("", h.listOwned)
	owned.POST("/imports", h.importOwned)
	owned.GET("/imports/:importId", h.getImportOwned)
	owned.GET("/imports/:importId/items", h.getImportItemsOwned)
	owned.GET("/imports/:importId/failures", h.getImportFailuresOwned)
	owned.POST("/bulk/:command", h.bulkOwned)
	owned.GET("/bulk-tasks/:taskId", h.getBulkOwned)
	owned.POST("/:resourceId/validate", h.commandOwned("validate"))
	owned.POST("/:resourceId/publish", h.commandOwned("publish"))
	owned.DELETE("/:resourceId", h.commandOwned("delete"))
	admin := rg.Group("/admin/daum/resources")
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
	admin.GET("/:resourceId/aliases", permission(checker, "read"), h.aliasesAdmin)
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
