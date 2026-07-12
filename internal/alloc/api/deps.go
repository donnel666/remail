package api

import (
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type Module struct {
	UseCase       *allocapp.UseCase
	Repo          *allocinfra.Repo
	ResourceGuard *ResourceAllocationGuardAdapter
}

func NewModule(db *gorm.DB, asynqClient *asynq.Client) *Module {
	repo := allocinfra.NewRepo(db)
	queue := allocinfra.NewCandidateRefreshQueue(asynqClient)
	useCase := allocapp.NewUseCase(repo, queue)
	useCase.SetAdminAllocationEnrichmentPort(allocinfra.NewAdminAllocationEnrichmentRepo(db))
	return &Module{
		UseCase:       useCase,
		Repo:          repo,
		ResourceGuard: NewResourceAllocationGuardAdapter(useCase),
	}
}
