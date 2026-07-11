package api

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
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

type BackgroundDispatchSizer interface {
	AcquireDispatchBudget(ctx context.Context, queue string, minimum, maximum int) (int, func())
	TryAcquireExecution(ctx context.Context, queue string) (bool, func())
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
	ValidationUseCase   coreapp.ResourceValidationPort
	ValidationAdapter   *ResourceValidationAdapter
	BindingRecorder     coreapp.MicrosoftBindingInputRecorder
	InboundSMTP         *mailinfra.InboundSMTPServer
	InboundSMTPEnabled  bool
	BackgroundDispatch  BackgroundDispatchSizer
	AliasDispatch       MicrosoftAliasDispatchReleaser
}

func (m *MailTransportModule) SetBackgroundDispatchSizer(sizer BackgroundDispatchSizer) {
	if m != nil {
		m.BackgroundDispatch = sizer
	}
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
	outboundStore := mailinfra.NewOutboundMailStore(db)
	outboundQueue := mailinfra.NewOutboundMailQueue(asynqClient)
	inboundRepo := mailinfra.NewInboundMailRepo(db)
	inboundResolver := mailinfra.NewInboundResourceResolver(db)
	inboundQueue := mailinfra.NewInboundMailQueue(asynqClient)
	bindingRepo := mailinfra.NewMicrosoftBindingRepo(db)
	aliasStore := mailinfra.NewMicrosoftAliasStore(db)
	aliasQueue := mailinfra.NewMicrosoftAliasQueue(asynqClient)
	msacl.SetMailboxReader(mailinfra.NewMSACLMailboxReader(db, files))

	inboundUseCase := mailapp.NewInboundService(inboundRepo, inboundResolver, files, inboundQueue, systemLogs)
	outboundDelivery := mailapp.NewAsyncDeliveryService(outboundStore, outboundQueue, systemLogs, outboundFrom)
	validationAdapter := NewResourceValidationAdapter(proxies, bindingRepo)
	aliasAdapter := NewMicrosoftAliasCreationAdapter(proxies)
	module := &MailTransportModule{
		DeliveryUseCase:     outboundDelivery,
		OutboundDelivery:    outboundDelivery,
		OutboundSendUseCase: mailapp.NewOutboundSendUseCase(outboundStore, sender, systemLogs),
		InboundUseCase:      inboundUseCase,
		MicrosoftAliases:    mailapp.NewMicrosoftAliasService(aliasStore, aliasQueue, aliasAdapter),
		ValidationUseCase:   validationAdapter,
		ValidationAdapter:   validationAdapter,
		BindingRecorder:     NewMicrosoftBindingInputAdapter(bindingRepo),
		InboundSMTPEnabled:  inboundCfg.Enabled,
		AliasDispatch:       aliasStore,
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
	go func() {
		mailTicker := time.NewTicker(mailDispatcherInterval)
		aliasTicker := time.NewTicker(microsoftAliasDispatcherInterval)
		defer mailTicker.Stop()
		defer aliasTicker.Stop()
		for {
			select {
			case <-mailTicker.C:
				scheduleMailDispatchers(ctx, m, 0)
			case <-aliasTicker.C:
				scheduleMicrosoftAliasDispatcher(ctx, m, 0)
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		once.Do(cancel)
	}
}
