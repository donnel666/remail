package api

import (
	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// CoreModule holds all wired dependencies for the Core (resource) module.
type CoreModule struct {
	ImportUseCase   *coreapp.ImportUseCase
	ResourceUseCase *coreapp.ResourceUseCase
	DomainUseCase   *coreapp.DomainUseCase
	ServerUseCase   *coreapp.ServerUseCase
	MailboxUseCase  *coreapp.DomainMailboxUseCase
}

// NewCoreModule wires up all Core module dependencies.
func NewCoreModule(db *gorm.DB, _ redis.UniversalClient, files governanceapp.FilePort) (*CoreModule, error) {
	txtParser := coreinfra.NewTXTParser()
	resourceRepo := coreinfra.NewResourceRepo(db)
	importRepo := coreinfra.NewResourceImportRepo(db)
	mailServerRepo := coreinfra.NewMailServerRepo(db)
	mailboxRepo := coreinfra.NewGeneratedMailboxRepo(db)

	return &CoreModule{
		ImportUseCase:   coreapp.NewImportUseCase(resourceRepo, importRepo, txtParser, files),
		ResourceUseCase: coreapp.NewResourceUseCase(resourceRepo),
		DomainUseCase:   coreapp.NewDomainUseCase(resourceRepo, mailServerRepo, mailboxRepo),
		ServerUseCase:   coreapp.NewServerUseCase(mailServerRepo),
		MailboxUseCase:  coreapp.NewDomainMailboxUseCase(mailboxRepo, resourceRepo),
	}, nil
}
