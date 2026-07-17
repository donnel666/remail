package api

import (
	"context"

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

type BackgroundExecutionGate interface {
	TryAcquire() (release func(), admitted bool)
}

type Module struct {
	UseCase             *mailmatchapp.UseCase
	ResourceFetch       *mailmatchapp.ResourceFetchUseCase
	ProjectHistory      *mailmatchapp.ProjectHistoryScanUseCase
	AdminMessages       *mailmatchapp.AdminMessageUseCase
	BackgroundExecution BackgroundExecutionGate
	resourceFetchRepo   *mailmatchinfra.ResourceFetchRepo
}

func (m *Module) SetBackgroundExecutionGate(gate BackgroundExecutionGate) {
	if m != nil {
		m.BackgroundExecution = gate
	}
}

func (m *Module) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if m == nil {
		return
	}
	if m.resourceFetchRepo != nil {
		m.resourceFetchRepo.SetMicrosoftCredentialPort(credentials)
	}
	if m.UseCase != nil {
		m.UseCase.SetMicrosoftCredentialPort(credentials)
	}
	if m.ProjectHistory != nil {
		m.ProjectHistory.SetMicrosoftCredentialPort(credentials)
	}
}

func NewModule(db *gorm.DB, files governanceapp.FilePort, asynqClient *asynq.Client, proxies *proxyapp.ProxyUseCase, trade *tradeapp.UseCase) *Module {
	repo := mailmatchinfra.NewRepo(db, files)
	resourceFetchRepo := mailmatchinfra.NewResourceFetchRepo(db)
	projectHistoryRepo := mailmatchinfra.NewProjectHistoryScanRepo(db)
	adminMessageRepo := mailmatchinfra.NewAdminMessageRepo(db)
	queue := mailmatchinfra.NewFetchQueue(asynqClient)
	transport := NewMicrosoftFetchAdapter(proxies)
	useCase := mailmatchapp.NewUseCase(repo, queue, transport, matchResultAdapter{trade: trade})
	projectHistory := mailmatchapp.NewProjectHistoryScanUseCase(projectHistoryRepo, repo, queue, transport)
	if trade != nil {
		projectHistory.SetHistoricalMicrosoftUsagePort(historicalMicrosoftUsageAdapter{trade: trade})
	}
	resourceFetch := mailmatchapp.NewResourceFetchUseCase(
		resourceFetchRepo,
		queue,
		transport,
		useCase,
		governanceinfra.NewSystemLogRepo(db),
	)
	resourceFetch.SetProjectHistoryScan(projectHistory)
	return &Module{
		UseCase:           useCase,
		ResourceFetch:     resourceFetch,
		ProjectHistory:    projectHistory,
		AdminMessages:     mailmatchapp.NewAdminMessageUseCase(adminMessageRepo),
		resourceFetchRepo: resourceFetchRepo,
	}
}

type historicalMicrosoftUsageAdapter struct {
	trade *tradeapp.UseCase
}

func (a historicalMicrosoftUsageAdapter) ImportHistoricalMicrosoftUsage(ctx context.Context, matches []mailmatchapp.HistoricalProjectMatch) error {
	if len(matches) == 0 {
		return nil
	}
	items := make([]tradeapp.HistoricalMicrosoftUsage, len(matches))
	for i := range matches {
		items[i] = tradeapp.HistoricalMicrosoftUsage{
			ResourceID: matches[i].ResourceID, ProjectID: matches[i].ProjectID, ProductID: matches[i].ProductID,
			Mailbox: string(matches[i].MailboxType), Email: matches[i].MailboxEmail,
			CodeWindowMinutes:       matches[i].CodeWindowMinutes,
			ActivationWindowMinutes: matches[i].ActivationWindowMinutes,
			WarrantyMinutes:         matches[i].WarrantyMinutes,
			FirstMatchedAt:          matches[i].FirstMatchedAt, LastMatchedAt: matches[i].LastMatchedAt,
			EvidenceCount: matches[i].EvidenceCount,
		}
	}
	return a.trade.ImportHistoricalMicrosoftUsage(ctx, items)
}
