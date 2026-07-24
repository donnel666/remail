package api

import (
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/systemsettings/app"
	"github.com/donnel666/remail/internal/systemsettings/infra"
	"gorm.io/gorm"
)

// Module contains the dependencies for the administrator system-settings API.
type Module struct {
	Settings *app.SystemSettingsUseCase
}

func NewModule(db *gorm.DB) *Module {
	return &Module{Settings: app.NewSystemSettingsUseCase(
		infra.NewRepository(db),
		governanceinfra.NewOperationLogRepo(db),
	)}
}
