package api

import (
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/donnel666/remail/internal/dashboard/infra"
	"gorm.io/gorm"
)

// Module bundles the console dashboard query service. It is self-contained: the
// ViewRepo reads every table it needs (orders, delivery heads, wallets,
// projects, users) directly, so no cross-module ports are wired.
type Module struct {
	Query *dashboardapp.QueryService
}

func NewModule(db *gorm.DB) *Module {
	return &Module{Query: dashboardapp.NewQueryService(infra.NewViewRepo(db))}
}
