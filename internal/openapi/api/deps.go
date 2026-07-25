package api

import (
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapiinfra "github.com/donnel666/remail/internal/openapi/infra"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Module struct {
	UseCase *openapiapp.UseCase
}

func NewModule(db *gorm.DB, redisClients ...redis.UniversalClient) *Module {
	repo := openapiinfra.NewRepo(db)
	if len(redisClients) > 0 && redisClients[0] != nil {
		return &Module{UseCase: openapiapp.NewUseCase(repo, openapiinfra.NewAPIKeyConcurrencyGate(redisClients[0]))}
	}
	return &Module{UseCase: openapiapp.NewUseCase(repo)}
}
