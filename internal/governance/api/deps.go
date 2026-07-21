package api

import (
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"gorm.io/gorm"
)

type Module struct {
	Tasks         *governanceapp.AdminTaskQueryService
	Logs          *governanceapp.AdminLogService
	CoreTaskQuery *CoreTaskQueryAdapter
}

func NewModule(db *gorm.DB) *Module {
	tasks := governanceapp.NewAdminTaskQueryService(governanceinfra.NewAdminTaskViewRepo(db))
	logs := governanceapp.NewAdminLogService(governanceinfra.NewAdminLogRepo(db))
	return &Module{
		Tasks:         tasks,
		Logs:          logs,
		CoreTaskQuery: NewCoreTaskQueryAdapter(tasks),
	}
}
