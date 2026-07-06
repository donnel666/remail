package api

import (
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type Module struct {
	UseCase *allocapp.UseCase
	Repo    *allocinfra.Repo
}

func NewModule(db *gorm.DB, asynqClient *asynq.Client) *Module {
	repo := allocinfra.NewRepo(db)
	queue := allocinfra.NewCandidateRefreshQueue(asynqClient)
	return &Module{
		UseCase: allocapp.NewUseCase(repo, queue),
		Repo:    repo,
	}
}
