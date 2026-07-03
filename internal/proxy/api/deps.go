package api

import (
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxyinfra "github.com/donnel666/remail/internal/proxy/infra"
	"gorm.io/gorm"
)

type ProxyModule struct {
	ProxyUseCase *proxyapp.ProxyUseCase
}

func NewProxyModule(db *gorm.DB) (*ProxyModule, error) {
	repo := proxyinfra.NewProxyRepo(db)
	checker := proxyinfra.NewProxyChecker()
	operationLogs := governanceinfra.NewOperationLogRepo(db)
	systemLogs := governanceinfra.NewSystemLogRepo(db)

	return &ProxyModule{
		ProxyUseCase: proxyapp.NewProxyUseCase(repo, checker, operationLogs, systemLogs),
	}, nil
}
