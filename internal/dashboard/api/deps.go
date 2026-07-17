package api

import (
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/donnel666/remail/internal/dashboard/infra"
	"gorm.io/gorm"
)

// Module bundles the console dashboard query service. It is self-contained: the
// ViewRepo reads every table it needs (orders, delivery heads, wallets,
// projects, users) directly, so no cross-module ports are wired. Admin support
// is added via SetAdminPorts (finance from billing, inventory from alloc).
type Module struct {
	Query      *dashboardapp.QueryService
	AdminQuery *dashboardapp.AdminQueryService
	adminView  dashboardapp.AdminView
}

func NewModule(db *gorm.DB) *Module {
	return &Module{
		Query:     dashboardapp.NewQueryService(infra.NewViewRepo(db)),
		adminView: infra.NewAdminViewRepo(db),
	}
}

// SetAdminPorts wires the cross-context data the admin dashboard needs and
// enables the /admin/dashboard route.
func (m *Module) SetAdminPorts(finance dashboardapp.AdminFinancePort, inventory dashboardapp.AdminInventoryPort) {
	m.AdminQuery = dashboardapp.NewAdminQueryService(m.adminView, finance, inventory)
}
