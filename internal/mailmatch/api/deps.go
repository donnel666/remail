package api

import (
	coreapp "github.com/donnel666/remail/internal/core/app"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type Module struct {
	UseCase           *mailmatchapp.UseCase
	ResourceFetch     *mailmatchapp.ResourceFetchUseCase
	AdminMessages     *mailmatchapp.AdminMessageUseCase
	resourceFetchRepo *mailmatchinfra.ResourceFetchRepo
}

func (m *Module) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if m == nil || m.resourceFetchRepo == nil {
		return
	}
	m.resourceFetchRepo.SetMicrosoftCredentialPort(credentials)
}

func NewModule(db *gorm.DB, files governanceapp.FilePort, asynqClient *asynq.Client, proxies *proxyapp.ProxyUseCase, trade *tradeapp.UseCase) *Module {
	repo := mailmatchinfra.NewRepo(db, files)
	resourceFetchRepo := mailmatchinfra.NewResourceFetchRepo(db)
	adminMessageRepo := mailmatchinfra.NewAdminMessageRepo(db)
	queue := mailmatchinfra.NewFetchQueue(asynqClient)
	transport := NewMicrosoftFetchAdapter(proxies)
	useCase := mailmatchapp.NewUseCase(repo, queue, transport, matchResultAdapter{trade: trade})
	return &Module{
		UseCase: useCase,
		ResourceFetch: mailmatchapp.NewResourceFetchUseCase(
			resourceFetchRepo,
			queue,
			transport,
			useCase,
			governanceinfra.NewSystemLogRepo(db),
		),
		AdminMessages:     mailmatchapp.NewAdminMessageUseCase(adminMessageRepo),
		resourceFetchRepo: resourceFetchRepo,
	}
}
