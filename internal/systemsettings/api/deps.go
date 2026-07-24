package api

import (
	"context"
	"log/slog"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/systemsettings/app"
	"github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

// Module contains the dependencies for the administrator system-settings API.
type Module struct {
	Settings *app.SystemSettingsUseCase
}

func NewModule(db *gorm.DB) (*Module, error) {
	repo := infra.NewRepository(db)
	settings, err := repo.List(context.Background())
	if err != nil {
		return nil, err
	}
	for _, setting := range settings {
		if err := runtimeconfig.Validate(setting.Key, setting.Value); err != nil {
			slog.Warn("ignoring invalid persisted runtime setting", "key", setting.Key, "error", err)
		}
	}
	if err := runtimeconfig.ValidateSnapshot(settings); err != nil {
		slog.Warn("ignoring conflicting persisted runtime settings", "error", err)
	}
	runtimeconfig.Replace(settings)
	return &Module{Settings: app.NewSystemSettingsUseCase(
		repo,
		governanceinfra.NewOperationLogRepo(db),
	)}, nil
}
