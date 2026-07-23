package api

import (
	"context"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type BackgroundExecutionGate interface {
	TryAcquire() (release func(), admitted bool)
}

type ProductInventoryProvider interface {
	GetProductInventorySnapshots(ctx context.Context, projectIDs []uint) (map[uint]*allocapp.ProjectProductInventoryTotals, error)
}

// CoreModule holds all wired dependencies for the Core (resource) module.
type CoreModule struct {
	ImportUseCase        *coreapp.ImportUseCase
	ResourceUseCase      *coreapp.ResourceUseCase
	ValidationUseCase    *coreapp.ResourceValidationUseCase
	DomainUseCase        *coreapp.DomainUseCase
	ServerUseCase        *coreapp.ServerUseCase
	MailboxUseCase       *coreapp.DomainMailboxUseCase
	ProjectUseCase       *coreapp.ProjectUseCase
	ProjectAssets        *coreapp.ProjectAssetUseCase
	ProductInventory     ProductInventoryProvider
	AdminResourceQuery   *coreapp.AdminResourceQuery
	AdminCommands        *coreapp.AdminResourceCommandService
	AdminBulk            *coreapp.AdminResourceBulkService
	AdminDomainQuery     *coreapp.AdminDomainQuery
	AdminDomainCommands  *coreapp.AdminDomainCommandService
	MicrosoftCredentials coreapp.MicrosoftCredentialPort
	BackgroundExecution  BackgroundExecutionGate
	validationRepo       *coreinfra.ResourceValidationRepo
}

func (m *CoreModule) SetBackgroundExecutionGate(gate BackgroundExecutionGate) {
	if m != nil {
		m.BackgroundExecution = gate
	}
}

func (m *CoreModule) SetAdminResourcePorts(
	owners coreapp.OwnerQueryPort,
	bindings coreapp.BindingQueryPort,
	bindingAdmin coreapp.BindingAdminPort,
	allocations coreapp.ResourceAllocationGuardPort,
	tasks coreapp.TaskQueryPort,
	aliases coreapp.AliasScheduleQueryPort,
) {
	if m == nil {
		return
	}
	if m.AdminResourceQuery != nil {
		m.AdminResourceQuery.SetPorts(owners, bindings, tasks, aliases)
	}
	if m.AdminCommands != nil {
		m.AdminCommands.SetPorts(owners, bindings, bindingAdmin, allocations)
	}
	if m.AdminDomainQuery != nil {
		m.AdminDomainQuery.SetPorts(owners, bindings)
	}
	if m.AdminDomainCommands != nil {
		m.AdminDomainCommands.SetPorts(owners, allocations)
	}
	if m.ProjectUseCase != nil {
		m.ProjectUseCase.SetOwnerQueryPort(owners)
	}
}

func (m *CoreModule) SetAdminProxyBindingQueryPort(port coreapp.AdminProxyBindingQueryPort) {
	if m != nil && m.AdminResourceQuery != nil {
		m.AdminResourceQuery.SetProxyBindings(port)
	}
}

func (m *CoreModule) SetMicrosoftAliasScheduleTrigger(trigger coreapp.MicrosoftAliasScheduleTriggerPort) {
	if m != nil && m.ValidationUseCase != nil {
		m.ValidationUseCase.SetMicrosoftAliasScheduleTrigger(trigger)
	}
}

func (m *CoreModule) SetMicrosoftHistoryScanTrigger(trigger coreapp.MicrosoftHistoryScanTriggerPort) {
	if m != nil && m.ValidationUseCase != nil {
		m.ValidationUseCase.SetMicrosoftHistoryScanTrigger(trigger)
	}
}

func (m *CoreModule) SetAdminResourceMaintenancePort(port coreapp.AdminResourceMaintenancePort) {
	if m != nil && m.AdminBulk != nil {
		m.AdminBulk.SetMaintenancePort(port)
	}
}

func (m *CoreModule) SetMicrosoftValidationBindingCommitPort(port coreapp.MicrosoftValidationBindingCommitPort) {
	if m != nil && m.validationRepo != nil {
		m.validationRepo.SetMicrosoftValidationBindingCommitPort(port)
	}
}

// NewCoreModule wires up all Core module dependencies.
func NewCoreModule(db *gorm.DB, redisClient redis.UniversalClient, files governanceapp.FilePort, asynqClient *asynq.Client, validator coreapp.ResourceValidationPort, bindingRecorder coreapp.MicrosoftBindingInputRecorder) (*CoreModule, error) {
	txtParser := coreinfra.NewTXTParser()
	resourceRepo := coreinfra.NewResourceRepo(db)
	importRepo := coreinfra.NewResourceImportRepo(db)
	importQueue := coreinfra.NewResourceImportQueue(asynqClient)
	validationRepo := coreinfra.NewResourceValidationRepo(db)
	validationQueue := coreinfra.NewResourceValidationQueue(asynqClient, redisClient)
	mailServerRepo := coreinfra.NewMailServerRepo(db)
	mailboxRepo := coreinfra.NewGeneratedMailboxRepo(db)
	projectRepo := coreinfra.NewProjectRepo(db)
	importUseCase := coreapp.NewImportUseCase(resourceRepo, importRepo, txtParser, files, importQueue, bindingRecorder)
	validationUseCase := coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, validationQueue, validator)
	adminRepo := coreinfra.NewAdminResourceRepo(db)
	adminQuery := coreapp.NewAdminResourceQuery(adminRepo)
	adminDomainQuery := coreapp.NewAdminDomainQuery(adminRepo)
	adminCommands := coreapp.NewAdminResourceCommandService(
		adminRepo,
		validationUseCase,
		governanceinfra.NewOperationLogRepo(db),
	)
	adminDomainCommands := coreapp.NewAdminDomainCommandService(
		adminRepo,
		mailServerRepo,
		validationUseCase,
		governanceinfra.NewOperationLogRepo(db),
	)
	adminDomainCommands.SetBulkQueue(coreinfra.NewAdminDomainBulkQueue(asynqClient, redisClient))
	adminBulk := coreapp.NewAdminResourceBulkService(
		coreinfra.NewAdminResourceBulkRepo(db),
		coreinfra.NewAdminResourceBulkQueue(asynqClient, redisClient),
		adminCommands,
	)

	return &CoreModule{
		ImportUseCase:        importUseCase,
		ResourceUseCase:      coreapp.NewResourceUseCase(resourceRepo),
		ValidationUseCase:    validationUseCase,
		DomainUseCase:        coreapp.NewDomainUseCase(resourceRepo, mailServerRepo, mailboxRepo),
		ServerUseCase:        coreapp.NewServerUseCase(mailServerRepo),
		MailboxUseCase:       coreapp.NewDomainMailboxUseCase(mailboxRepo, resourceRepo),
		ProjectUseCase:       coreapp.NewProjectUseCase(projectRepo),
		ProjectAssets:        coreapp.NewProjectAssetUseCase(files),
		AdminResourceQuery:   adminQuery,
		AdminCommands:        adminCommands,
		AdminBulk:            adminBulk,
		AdminDomainQuery:     adminDomainQuery,
		AdminDomainCommands:  adminDomainCommands,
		MicrosoftCredentials: coreapp.NewMicrosoftCredentialService(adminRepo),
		validationRepo:       validationRepo,
	}, nil
}
