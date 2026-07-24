package api

import (
	"context"
	"log/slog"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/systemsettings/app"
	"github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Module contains the dependencies for the administrator system-settings API.
type Module struct {
	Settings    *app.SystemSettingsUseCase
	runtimeSync *runtimeSettingsSync
}

func NewModule(db *gorm.DB, redisClients ...redis.UniversalClient) (*Module, error) {
	var redisClient redis.UniversalClient
	if len(redisClients) > 0 {
		redisClient = redisClients[0]
	}
	ctx := context.Background()
	repo := infra.NewRepository(db)
	if err := repo.InsertMissing(ctx, runtimeconfig.DefaultSettings()); err != nil {
		return nil, err
	}
	settings, err := repo.List(ctx)
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
	useCase := app.NewSystemSettingsUseCase(
		repo,
		governanceinfra.NewOperationLogRepo(db),
	)
	module := &Module{
		Settings:    useCase,
		runtimeSync: newRuntimeSettingsSync(redisClient, repo),
	}
	useCase.SetRuntimeSettingsPublisher(newRedisRuntimeSettingsPublisher(redisClient))
	return module, nil
}

// Start begins cross-replica runtime setting invalidation. Close is returned
// in the same shape as the router's other background cleanup functions.
func (m *Module) Start(ctx context.Context) func(context.Context) {
	if m == nil || m.runtimeSync == nil {
		return func(context.Context) {}
	}
	m.runtimeSync.Start(ctx)
	return m.Close
}

func (m *Module) Close(ctx context.Context) {
	if m != nil && m.runtimeSync != nil {
		m.runtimeSync.Close(ctx)
	}
}
