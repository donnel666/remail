package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type BackgroundExecutionGate interface {
	TryAcquire() (release func(), admitted bool)
}

type Module struct {
	UseCase                *mailmatchapp.UseCase
	AdminResourceFetch     *mailmatchapp.AdminResourceFetchUseCase
	ResourceHistory        *mailmatchapp.ResourceHistoryUseCase
	ProjectHistory         *mailmatchapp.ProjectHistoryScanUseCase
	AdminMessages          *mailmatchapp.AdminMessageUseCase
	BotDiagnosis           *mailmatchapp.BotDiagnosisService
	BackgroundExecution    BackgroundExecutionGate
	adminResourceFetchRepo *mailmatchinfra.AdminResourceFetchRepo
	resourceHistoryRepo    *mailmatchinfra.ResourceHistoryRepo
	matchResults           *matchResultAdapter
	UpstreamPickup         UpstreamPickupPort
}

type UpstreamPickupPort interface {
	ReadUpstreamPickup(ctx context.Context, email, token string) ([]mailmatchdomain.MailContent, bool, error)
}

func (m *Module) SetUpstreamPickup(port UpstreamPickupPort) {
	if m != nil {
		m.UpstreamPickup = port
	}
}

func (m *Module) SetBotDiagnosisRefresh(port mailmatchapp.CodeDiagnosisRefreshPort) {
	if m != nil && m.BotDiagnosis != nil {
		m.BotDiagnosis.SetRefresh(port)
	}
}

func (m *Module) SetGmailMatchPort(port GmailMatchPort) {
	if m != nil && m.matchResults != nil {
		m.matchResults.gmail = port
	}
}

func (m *Module) SetGmailPurchaseFetchPort(port mailmatchapp.GmailPurchaseFetchPort) {
	if m != nil && m.UseCase != nil {
		m.UseCase.SetGmailPurchaseFetchPort(port)
	}
}

func (m *Module) SetICloudMailFetchPort(port mailmatchapp.ICloudMailFetchPort) {
	if m != nil && m.UseCase != nil {
		m.UseCase.SetICloudMailFetchPort(port)
	}
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
	if m.adminResourceFetchRepo != nil {
		m.adminResourceFetchRepo.SetMicrosoftCredentialPort(credentials)
	}
	if m.resourceHistoryRepo != nil {
		m.resourceHistoryRepo.SetMicrosoftCredentialPort(credentials)
	}
	if m.UseCase != nil {
		m.UseCase.SetMicrosoftCredentialPort(credentials)
	}
	if m.ProjectHistory != nil {
		m.ProjectHistory.SetMicrosoftCredentialPort(credentials)
	}
}

func NewModule(db *gorm.DB, files governanceapp.FilePort, redisClient redis.UniversalClient, asynqClient *asynq.Client, proxies *proxyapp.ProxyUseCase, trade *tradeapp.UseCase, validation *coreapp.ResourceValidationUseCase) *Module {
	repo := mailmatchinfra.NewRepo(db, files)
	adminResourceFetchRepo := mailmatchinfra.NewAdminResourceFetchRepo(db)
	resourceHistoryRepo := mailmatchinfra.NewResourceHistoryRepo(db)
	projectHistoryRepo := mailmatchinfra.NewProjectHistoryScanRepo(db)
	adminMessageRepo := mailmatchinfra.NewAdminMessageRepo(db)
	queue := mailmatchinfra.NewFetchQueue(asynqClient)
	transport := NewMicrosoftFetchAdapter(proxies, redisClient)
	if validation != nil && trade != nil {
		transport.SetPermanentMicrosoftFetchFailurePort(permanentMicrosoftFetchFailureAdapter{validation: validation, trade: trade})
	}
	matchResults := &matchResultAdapter{trade: trade}
	useCase := mailmatchapp.NewUseCase(repo, queue, transport, matchResults)
	useCase.SetPickupFetchStatePort(mailmatchinfra.NewPickupFetchState(redisClient))
	useCase.SetPickupMessageCachePort(mailmatchinfra.NewPickupMessageCache(redisClient))
	projectHistory := mailmatchapp.NewProjectHistoryScanUseCase(projectHistoryRepo, repo, queue, transport)
	if trade != nil {
		projectHistory.SetHistoricalMicrosoftUsagePort(historicalMicrosoftUsageAdapter{trade: trade})
	}
	systemLogs := governanceinfra.NewSystemLogRepo(db)
	adminResourceFetch := mailmatchapp.NewAdminResourceFetchUseCase(
		adminResourceFetchRepo,
		queue,
		transport,
		useCase,
		systemLogs,
	)
	resourceHistory := mailmatchapp.NewResourceHistoryUseCase(resourceHistoryRepo, queue, projectHistory, systemLogs)
	return &Module{
		UseCase:                useCase,
		AdminResourceFetch:     adminResourceFetch,
		ResourceHistory:        resourceHistory,
		ProjectHistory:         projectHistory,
		AdminMessages:          mailmatchapp.NewAdminMessageUseCase(adminMessageRepo),
		BotDiagnosis:           mailmatchapp.NewBotDiagnosisService(repo, useCase),
		adminResourceFetchRepo: adminResourceFetchRepo,
		resourceHistoryRepo:    resourceHistoryRepo,
		matchResults:           matchResults,
	}
}

type permanentMicrosoftFetchFailureAdapter struct {
	validation *coreapp.ResourceValidationUseCase
	trade      *tradeapp.UseCase
}

func (a permanentMicrosoftFetchFailureAdapter) HandlePermanentMicrosoftFetchFailure(ctx context.Context, failure mailmatchapp.PermanentMicrosoftFetchFailure) error {
	abnormal, err := a.validation.RecordMicrosoftFetchFailure(
		ctx,
		failure.ResourceID,
		failure.CredentialRevision,
		failure.RefreshToken,
		failure.Category,
		failure.SafeMessage,
		failure.RequestID,
		failure.FailureCount >= microsoftFetchFailureThreshold,
	)
	if err != nil || !abnormal {
		return err
	}
	_, err = a.trade.RefundUnavailableMicrosoftOrders(ctx, failure.ResourceID, failure.RequestID)
	return err
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
