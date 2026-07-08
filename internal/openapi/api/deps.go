package api

import (
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	openapiinfra "github.com/donnel666/remail/internal/openapi/infra"
	"gorm.io/gorm"
)

type Module struct {
	UseCase *openapiapp.UseCase
}

func NewModule(db *gorm.DB) *Module {
	repo := openapiinfra.NewRepo(db)
	return &Module{UseCase: openapiapp.NewUseCase(repo)}
}
