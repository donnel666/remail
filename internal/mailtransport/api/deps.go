package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	coreinfra "github.com/donnel666/remail/internal/core/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	smtpserver "github.com/emersion/go-smtp"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type BackgroundExecutionGate interface {
	TryAcquire() (release func(), admitted bool)
}

type MicrosoftAliasDispatchReleaser interface {
	MarkDispatchFailed(ctx context.Context, task mailapp.MicrosoftAliasTask, nextRunAt time.Time, safeError string) error
}

type MailTransportModule struct {
	DeliveryUseCase     mailapp.DeliveryPort
	OutboundDelivery    *mailapp.AsyncDeliveryService
	OutboundSendUseCase *mailapp.OutboundSendUseCase
	InboundUseCase      *mailapp.InboundService
	MicrosoftAliases    *mailapp.MicrosoftAliasService
	TokenRefresh        *mailapp.MicrosoftTokenRefreshService
	AuxiliaryMail       mailapp.AuxiliaryMailQueryPort
	AliasScheduleQuery  coreapp.AliasScheduleQueryPort
	ValidationUseCase   coreapp.ResourceValidationPort
	ValidationAdapter   *ResourceValidationAdapter
	BindingRecorder     coreapp.MicrosoftBindingInputRecorder
	BindingQuery        coreapp.BindingQueryPort
	BindingAdmin        coreapp.BindingAdminPort
	ValidationBinding   coreapp.MicrosoftValidationBindingCommitPort
	InboundSMTP         *mailinfra.InboundSMTPServer
	InboundSMTPEnabled  bool
	BackgroundExecution BackgroundExecutionGate
	AliasDispatch       MicrosoftAliasDispatchReleaser
	tokenRefreshRepo    *mailinfra.MicrosoftTokenRefreshRepo
	bindingDomains      bindingDomainLister
	autoRefresh         microsoftAutoRefreshLister
}

func (m *MailTransportModule) SetBackgroundExecutionGate(gate BackgroundExecutionGate) {
	if m != nil {
		m.BackgroundExecution = gate
	}
}

// bindingDomainLister sources the auxiliary/recovery-mailbox domains
// (domain_resources.purpose='binding') injected into msacl.
type bindingDomainLister interface {
	ListBindingDomains(ctx context.Context) ([]string, error)
}

// microsoftAutoRefreshLister sources Microsoft resources whose refresh token is
// nearing expiry, for the proactive daily refresh scan.
type microsoftAutoRefreshLister interface {
	ListMicrosoftAutoRefreshCandidates(ctx context.Context, before time.Time, limit int) ([]coreinfra.MicrosoftAutoRefreshCandidate, error)
}

const (
	// microsoftBindingRecoveryHistoryWindow lets the validation safeguard use
	// older inbound Microsoft security-mail evidence when reconstructing a
	// recovery mailbox. Exact OTP List queries are unaffected by this window.
	microsoftBindingRecoveryHistoryWindow = 90 * 24 * time.Hour

	// auxiliaryDomainRefreshInterval controls how often the binding-domain list
	// is reloaded from the DB into msacl (eventually consistent; a newly-added
	// binding domain becomes usable within one interval).
	auxiliaryDomainRefreshInterval = 60 * time.Second

	// Proactive refresh-token expiry scan: once a day at ~dawn, enqueue a
	// refresh for every account whose refresh token expires within the lookahead
	// window (RT lifetime is ~3 months; start renewing a month ahead).
	microsoftRTRefreshLookahead = 30 * 24 * time.Hour
	microsoftRTRefreshHour      = 3
	microsoftRTRefreshScanLimit = 2000
)

// refreshAuxiliaryDomains loads the binding-purpose domains from the DB into the
// msacl auxiliary-domain list. On error it leaves the previous list in place.
func refreshAuxiliaryDomains(ctx context.Context, lister bindingDomainLister) {
	if lister == nil {
		return
	}
	domains, err := lister.ListBindingDomains(ctx)
	if err != nil {
		slog.Warn("load auxiliary binding domains failed", "error", err)
		return
	}
	msacl.SetAuxiliaryDomains(domains)
	slog.Info("auxiliary binding domains loaded", "count", len(domains))
}

// scanExpiringTokenRefresh enqueues a proactive refresh task for every Microsoft
// account whose refresh token expires within the lookahead window. Idempotent
// per-day (CreateOrReuse dedupes by IdempotencyKey), so re-runs are safe.
func (m *MailTransportModule) scanExpiringTokenRefresh(ctx context.Context) {
	if m == nil || m.autoRefresh == nil || m.TokenRefresh == nil {
		return
	}
	before := time.Now().UTC().Add(microsoftRTRefreshLookahead)
	candidates, err := m.autoRefresh.ListMicrosoftAutoRefreshCandidates(ctx, before, microsoftRTRefreshScanLimit)
	if err != nil {
		slog.Warn("microsoft rt auto-refresh scan failed", "error", err)
		return
	}
	day := time.Now().UTC().Format("20060102")
	enqueued := 0
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		if c.ResourceID == 0 || c.OwnerUserID == 0 {
			continue
		}
		if _, err := m.TokenRefresh.Accept(ctx, mailapp.MicrosoftTokenRefreshCommand{
			ResourceID:     c.ResourceID,
			OperatorUserID: c.OwnerUserID,
			IdempotencyKey: fmt.Sprintf("auto-rt-refresh-%d-%s", c.ResourceID, day),
			RequestID:      fmt.Sprintf("auto-rt-%s-%d", day, c.ResourceID),
			Path:           "system/auto-rt-refresh",
		}); err != nil {
			slog.Warn("microsoft rt auto-refresh enqueue failed", "resource_id", c.ResourceID, "error", err)
			continue
		}
		enqueued++
	}
	slog.Info("microsoft rt auto-refresh scan done", "candidates", len(candidates), "enqueued", enqueued)
}

// durationUntilHour returns the time until the next occurrence of the given hour
// (0-23) in the server's local time.
func durationUntilHour(hour int) time.Duration {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

func (m *MailTransportModule) SetInboundConsumer(consumer mailapp.InboundConsumerPort) {
	if m == nil || m.InboundUseCase == nil {
		return
	}
	m.InboundUseCase.SetConsumer(consumer)
}

func (m *MailTransportModule) SetHistoricalProjectMatcher(matcher mailapp.HistoricalProjectMatcher) {
	if m == nil || m.ValidationAdapter == nil {
		return
	}
	m.ValidationAdapter.SetHistoricalProjectMatcher(matcher)
}

func (m *MailTransportModule) SetMicrosoftCredentialPort(credentials coreapp.MicrosoftCredentialPort) {
	if m == nil || m.tokenRefreshRepo == nil {
		return
	}
	m.tokenRefreshRepo.SetMicrosoftCredentialPort(credentials)
}

func NewMailTransportModule(
	db *gorm.DB,
	files governanceapp.FilePort,
	asynqClient *asynq.Client,
	sender mailapp.SenderPort,
	outboundFrom string,
	inboundCfg mailinfra.InboundSMTPConfig,
	proxies *proxyapp.ProxyUseCase,
) (*MailTransportModule, error) {
	systemLogs := governanceinfra.NewSystemLogRepo(db)
	operationLogs := governanceinfra.NewOperationLogRepo(db)
	outboundStore := mailinfra.NewOutboundMailStore(db)
	outboundQueue := mailinfra.NewOutboundMailQueue(asynqClient)
	inboundRepo := mailinfra.NewInboundMailRepo(db)
	inboundResolver := mailinfra.NewInboundResourceResolver(db)
	inboundQueue := mailinfra.NewInboundMailQueue(asynqClient)
	bindingRepo := mailinfra.NewMicrosoftBindingRepo(db)
	recoveryLeaseStore := mailinfra.NewMicrosoftBindingRecoveryLeaseStore(db)
	auxiliaryMailRepo := mailinfra.NewAuxiliaryMailRepo(db)
	aliasStore := mailinfra.NewMicrosoftAliasStore(db)
	aliasQueue := mailinfra.NewMicrosoftAliasQueue(asynqClient)
	tokenRefreshRepo := mailinfra.NewMicrosoftTokenRefreshRepo(db)
	tokenRefreshQueue := mailinfra.NewMicrosoftTokenRefreshQueue(asynqClient)
	msacl.SetMailboxReader(mailinfra.NewMSACLMailboxReaderWithContentWindow(
		db,
		files,
		microsoftBindingRecoveryHistoryWindow,
	))
	msacl.SetRecoveryLeaseStore(recoveryLeaseStore)
	// Source the auxiliary (recovery) mailbox domains from domain_resources
	// (purpose='binding') instead of a hardcoded default; load once now and
	// refresh periodically in StartDispatchers. The same repo also feeds the
	// proactive refresh-token expiry scan.
	resourceRepo := coreinfra.NewResourceRepo(db)
	refreshAuxiliaryDomains(context.Background(), resourceRepo)

	inboundUseCase := mailapp.NewInboundService(inboundRepo, inboundResolver, files, inboundQueue, systemLogs)
	outboundDelivery := mailapp.NewAsyncDeliveryService(outboundStore, outboundQueue, systemLogs, outboundFrom)
	validationAdapter := NewResourceValidationAdapter(proxies, bindingRepo)
	aliasAdapter := NewMicrosoftAliasCreationAdapter(proxies)
	aliasService := mailapp.NewMicrosoftAliasService(aliasStore, aliasQueue, aliasAdapter)
	tokenRefreshService := mailapp.NewMicrosoftTokenRefreshService(tokenRefreshRepo, tokenRefreshQueue, validationAdapter)
	module := &MailTransportModule{
		DeliveryUseCase:     outboundDelivery,
		OutboundDelivery:    outboundDelivery,
		OutboundSendUseCase: mailapp.NewOutboundSendUseCase(outboundStore, sender, systemLogs),
		InboundUseCase:      inboundUseCase,
		MicrosoftAliases:    aliasService,
		TokenRefresh:        tokenRefreshService,
		AuxiliaryMail:       mailapp.NewAuxiliaryMailQueryService(auxiliaryMailRepo, bindingRepo, files, operationLogs, systemLogs),
		AliasScheduleQuery:  NewMicrosoftAliasScheduleQueryAdapter(aliasService),
		ValidationUseCase:   validationAdapter,
		ValidationAdapter:   validationAdapter,
		BindingRecorder:     NewMicrosoftBindingInputAdapter(bindingRepo),
		BindingQuery:        NewMicrosoftBindingQueryAdapter(bindingRepo),
		BindingAdmin:        NewMicrosoftBindingAdminAdapter(bindingRepo),
		ValidationBinding:   NewMicrosoftValidationBindingCommitAdapter(bindingRepo),
		InboundSMTPEnabled:  inboundCfg.Enabled,
		AliasDispatch:       aliasStore,
		tokenRefreshRepo:    tokenRefreshRepo,
		bindingDomains:      resourceRepo,
		autoRefresh:         resourceRepo,
	}
	if inboundCfg.Enabled {
		module.InboundSMTP = mailinfra.NewInboundSMTPServer(inboundCfg, inboundUseCase)
	}
	return module, nil
}

func (m *MailTransportModule) Start(ctx context.Context) func(context.Context) {
	smtpCleanup := m.StartInboundSMTP()
	dispatcherCleanup := m.StartDispatchers(ctx)
	return func(ctx context.Context) {
		dispatcherCleanup()
		smtpCleanup(ctx)
	}
}

func (m *MailTransportModule) StartInboundSMTP() func(context.Context) {
	if m == nil || !m.InboundSMTPEnabled || m.InboundSMTP == nil {
		return func(context.Context) {}
	}
	go func() {
		if err := m.InboundSMTP.ListenAndServe(); err != nil {
			if errors.Is(err, smtpserver.ErrServerClosed) {
				return
			}
			slog.Warn("inbound smtp server stopped", "error", err)
		}
	}()
	var once sync.Once
	return func(ctx context.Context) {
		once.Do(func() {
			_ = m.InboundSMTP.Shutdown(ctx)
		})
	}
}

func (m *MailTransportModule) StartDispatchers(ctx context.Context) func() {
	if m == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	scheduleMailDispatchers(ctx, m, 0)
	scheduleMicrosoftAliasDispatcher(ctx, m, 0)
	scheduleMicrosoftTokenRefreshDispatcher(ctx, m, 0)
	go func() {
		mailTicker := time.NewTicker(mailDispatcherInterval)
		aliasTicker := time.NewTicker(microsoftAliasDispatcherInterval)
		tokenRefreshTicker := time.NewTicker(microsoftTokenRefreshDispatcherInterval)
		bindingDomainTicker := time.NewTicker(auxiliaryDomainRefreshInterval)
		defer mailTicker.Stop()
		defer aliasTicker.Stop()
		defer tokenRefreshTicker.Stop()
		defer bindingDomainTicker.Stop()
		for {
			select {
			case <-mailTicker.C:
				scheduleMailDispatchers(ctx, m, 0)
			case <-aliasTicker.C:
				scheduleMicrosoftAliasDispatcher(ctx, m, 0)
			case <-tokenRefreshTicker.C:
				scheduleMicrosoftTokenRefreshDispatcher(ctx, m, 0)
			case <-bindingDomainTicker.C:
				refreshAuxiliaryDomains(ctx, m.bindingDomains)
			case <-ctx.Done():
				return
			}
		}
	}()
	// Proactive refresh-token expiry scan: once a day at ~dawn.
	go func() {
		for {
			select {
			case <-time.After(durationUntilHour(microsoftRTRefreshHour)):
				m.scanExpiringTokenRefresh(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		once.Do(cancel)
	}
}
