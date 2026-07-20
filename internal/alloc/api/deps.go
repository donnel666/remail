package api

import (
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Module struct {
	UseCase             *allocapp.UseCase
	Repo                *allocinfra.Repo
	ResourceGuard       *ResourceAllocationGuardAdapter
	BackgroundExecution BackgroundExecutionGate
}

type BackgroundExecutionGate interface {
	TryAcquire() (release func(), admitted bool)
}

func (m *Module) SetBackgroundExecutionGate(gate BackgroundExecutionGate) {
	if m != nil {
		m.BackgroundExecution = gate
	}
}

func NewModule(db *gorm.DB, redisClient redis.UniversalClient, asynqClient *asynq.Client) *Module {
	repo := allocinfra.NewRepo(db)
	queue := allocinfra.NewCandidateRefreshQueue(asynqClient)
	useCase := allocapp.NewUseCase(repo, queue)
	if redisClient != nil {
		useCase.SetInventoryCache(allocinfra.NewInventoryCache(redisClient))
	}
	useCase.SetAdminAllocationEnrichmentPort(allocinfra.NewAdminAllocationEnrichmentRepo(db))
	return &Module{
		UseCase:       useCase,
		Repo:          repo,
		ResourceGuard: NewResourceAllocationGuardAdapter(useCase),
	}
}
