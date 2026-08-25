package api

import (
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type BillingModule struct {
	WalletUseCase         *billingapp.WalletUseCase
	RechargeUseCase       *billingapp.RechargeUseCase
	OperationLogs         governanceapp.OperationLogPort
	UserSelectionResolver billingapp.UserSelectionResolver
	UserDirectory         billingapp.UserDirectory
}

func NewBillingModule(db *gorm.DB, clients ...*asynq.Client) *BillingModule {
	repo := billinginfra.NewBillingRepo(db)
	operationLogs := governanceinfra.NewOperationLogRepo(db)
	var client *asynq.Client
	if len(clients) > 0 {
		client = clients[0]
	}
	configProvider := billinginfra.RechargeConfigProvider{}
	return &BillingModule{
		WalletUseCase:   billingapp.NewWalletUseCase(repo),
		RechargeUseCase: billingapp.NewRechargeUseCase(repo, configProvider, billinginfra.NewRechargeGateway(configProvider), billinginfra.NewRechargeQueue(client)),
		OperationLogs:   operationLogs,
	}
}

// SetUserSelectionResolver wires the cross-context resolver used by bulk
// wallet adjustment. It is set after construction because the concrete
// implementation lives in the IAM package.
func (m *BillingModule) SetUserSelectionResolver(r billingapp.UserSelectionResolver) {
	m.UserSelectionResolver = r
}

// SetUserDirectory wires the IAM-backed identity source used to enrich finance
// read models (cards, transactions, wallets) and drive the balances list. Set
// after construction because the concrete implementation lives in IAM.
func (m *BillingModule) SetUserDirectory(d billingapp.UserDirectory) {
	m.UserDirectory = d
	m.WalletUseCase.SetUserDirectory(d)
}

func (m *BillingModule) SetMailDelivery(delivery mailapp.DeliveryPort) {
	m.WalletUseCase.SetMailDelivery(delivery)
	m.RechargeUseCase.SetNotifications(delivery, m.UserDirectory, m.WalletUseCase)
}
