package api

import (
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/donnel666/remail/internal/dashboard/infra"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
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
	adminCache *adminDashboardCache
	view       *infra.ViewRepo
	asynq      *asynq.Client
}

func NewModule(db *gorm.DB, redisClient redis.UniversalClient, asynqClient *asynq.Client) *Module {
	view := infra.NewViewRepo(db, redisClient)
	return &Module{
		Query:      dashboardapp.NewQueryService(view),
		adminView:  infra.NewAdminViewRepo(db),
		adminCache: newAdminDashboardCache(redisClient),
		view:       view,
		asynq:      asynqClient,
	}
}

// SetAdminPorts wires the cross-context data the admin dashboard needs and
// enables the /admin/dashboard route.
func (m *Module) SetAdminPorts(finance dashboardapp.AdminFinancePort, inventory dashboardapp.AdminInventoryPort) {
	m.AdminQuery = dashboardapp.NewAdminQueryService(m.adminView, finance, inventory)
}
