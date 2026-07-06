package api

import (
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billinginfra "github.com/donnel666/remail/internal/billing/infra"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"gorm.io/gorm"
)

type BillingModule struct {
	WalletUseCase *billingapp.WalletUseCase
	OperationLogs governanceapp.OperationLogPort
}

func NewBillingModule(db *gorm.DB) *BillingModule {
	repo := billinginfra.NewBillingRepo(db)
	operationLogs := governanceinfra.NewOperationLogRepo(db)
	return &BillingModule{
		WalletUseCase: billingapp.NewWalletUseCase(repo),
		OperationLogs: operationLogs,
	}
}
