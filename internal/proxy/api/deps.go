package api

import (
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxyinfra "github.com/donnel666/remail/internal/proxy/infra"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type ProxyModule struct {
	ProxyUseCase          *proxyapp.ProxyUseCase
	AdminResourceBindings *proxyapp.AdminResourceProxyBindingQuery
}

func NewProxyModule(db *gorm.DB, asynqClient *asynq.Client) (*ProxyModule, error) {
	repo := proxyinfra.NewProxyRepo(db)
	checker := proxyinfra.NewProxyChecker()
	checkQueue := proxyinfra.NewProxyCheckQueue(asynqClient)
	operationLogs := governanceinfra.NewOperationLogRepo(db)
	systemLogs := governanceinfra.NewSystemLogRepo(db)

	return &ProxyModule{
		ProxyUseCase:          proxyapp.NewProxyUseCase(repo, checker, checkQueue, operationLogs, systemLogs),
		AdminResourceBindings: proxyapp.NewAdminResourceProxyBindingQuery(repo),
	}, nil
}
