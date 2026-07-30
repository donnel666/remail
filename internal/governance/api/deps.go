package api

import (
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Module struct {
	Tasks         *governanceapp.AdminTaskQueryService
	Logs          *governanceapp.AdminLogService
	CoreTaskQuery *CoreTaskQueryAdapter
}

func NewModule(db *gorm.DB, redisClients ...redis.UniversalClient) *Module {
	tasks := governanceapp.NewAdminTaskQueryService(governanceinfra.NewAdminTaskViewRepo(db, redisClients...))
	logs := governanceapp.NewAdminLogService(governanceinfra.NewAdminLogRepo(db))
	return &Module{
		Tasks:         tasks,
		Logs:          logs,
		CoreTaskQuery: NewCoreTaskQueryAdapter(tasks),
	}
}
