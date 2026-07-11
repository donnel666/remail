package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type BackgroundDispatchSizer interface {
	AcquireDispatchBudget(ctx context.Context, queue string, minimum, maximum int) (int, func())
	TryAcquireExecution(ctx context.Context, queue string) (bool, func())
}

// CoreModule holds all wired dependencies for the Core (resource) module.
type CoreModule struct {
	ImportUseCase      *coreapp.ImportUseCase
	ResourceUseCase    *coreapp.ResourceUseCase
	ValidationUseCase  *coreapp.ResourceValidationUseCase
	DomainUseCase      *coreapp.DomainUseCase
	ServerUseCase      *coreapp.ServerUseCase
	MailboxUseCase     *coreapp.DomainMailboxUseCase
	ProjectUseCase     *coreapp.ProjectUseCase
	ProjectAssets      *coreapp.ProjectAssetUseCase
	BackgroundDispatch BackgroundDispatchSizer
}

func (m *CoreModule) SetBackgroundDispatchSizer(sizer BackgroundDispatchSizer) {
	if m != nil {
		m.BackgroundDispatch = sizer
	}
}

// NewCoreModule wires up all Core module dependencies.
func NewCoreModule(db *gorm.DB, _ redis.UniversalClient, files governanceapp.FilePort, asynqClient *asynq.Client, validator coreapp.ResourceValidationPort, bindingRecorder coreapp.MicrosoftBindingInputRecorder) (*CoreModule, error) {
	txtParser := coreinfra.NewTXTParser()
	resourceRepo := coreinfra.NewResourceRepo(db)
	importRepo := coreinfra.NewResourceImportRepo(db)
	importQueue := coreinfra.NewResourceImportQueue(asynqClient)
	validationRepo := coreinfra.NewResourceValidationRepo(db)
	validationQueue := coreinfra.NewResourceValidationQueue(asynqClient)
	mailServerRepo := coreinfra.NewMailServerRepo(db)
	mailboxRepo := coreinfra.NewGeneratedMailboxRepo(db)
	projectRepo := coreinfra.NewProjectRepo(db)
	importUseCase := coreapp.NewImportUseCase(resourceRepo, importRepo, txtParser, files, importQueue, bindingRecorder)
	importUseCase.SetImportedValidationCreator(validationRepo)

	return &CoreModule{
		ImportUseCase:     importUseCase,
		ResourceUseCase:   coreapp.NewResourceUseCase(resourceRepo),
		ValidationUseCase: coreapp.NewResourceValidationUseCase(resourceRepo, validationRepo, validationQueue, validator),
		DomainUseCase:     coreapp.NewDomainUseCase(resourceRepo, mailServerRepo, mailboxRepo),
		ServerUseCase:     coreapp.NewServerUseCase(mailServerRepo),
		MailboxUseCase:    coreapp.NewDomainMailboxUseCase(mailboxRepo, resourceRepo),
		ProjectUseCase:    coreapp.NewProjectUseCase(projectRepo),
		ProjectAssets:     coreapp.NewProjectAssetUseCase(files),
	}, nil
}
