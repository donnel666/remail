package api

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRuntimeSettingsSyncReloadsPublishedChanges(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	db, err := gorm.Open(sqlite.Open("file:system-settings-runtime-sync?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&infra.SettingModel{}))

	module, err := NewModule(db, redisClient)
	require.NoError(t, err)
	stop := module.Start(context.Background())
	t.Cleanup(func() { stop(context.Background()) })
	t.Cleanup(func() { runtimeconfig.Replace(nil) })

	repo := infra.NewRepository(db)
	_, err = repo.Upsert(context.Background(), "smtp_task_retry_count", "4")
	require.NoError(t, err)
	runtimeconfig.Replace(nil)

	require.Eventually(t, func() bool {
		_ = redisClient.Publish(context.Background(), runtimeSettingsChannel, `{"keys":["smtp_task_retry_count"]}`).Err()
		return runtimeconfig.Int("smtp_task_retry_count", 3, 0) == 4
	}, time.Second, 10*time.Millisecond)
}
