package api

import (
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type Module struct {
	UseCase *mailmatchapp.UseCase
	OpenAPI *openapiapp.UseCase
}

func NewModule(db *gorm.DB, files governanceapp.FilePort, asynqClient *asynq.Client, proxies *proxyapp.ProxyUseCase, trade *tradeapp.UseCase, tokens *openapiapp.UseCase) *Module {
	repo := mailmatchinfra.NewRepo(db, files)
	queue := mailmatchinfra.NewFetchQueue(asynqClient)
	return &Module{
		UseCase: mailmatchapp.NewUseCase(repo, queue, NewMicrosoftFetchAdapter(proxies), matchResultAdapter{trade: trade}),
		OpenAPI: tokens,
	}
}
