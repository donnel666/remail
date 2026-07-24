package api

import (
	"context"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNewModuleSeedsMissingDefaultsBeforeLoadingSnapshot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:system-settings-module?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&infra.SettingModel{}))
	require.NoError(t, db.Create(&infra.SettingModel{Key: "smtp_task_retry_count", Value: "4"}).Error)
	t.Cleanup(func() { runtimeconfig.Replace(nil) })

	_, err = NewModule(db, nil)
	require.NoError(t, err)
	require.Equal(t, 4, runtimeconfig.Int("smtp_task_retry_count", 3, 0))
	require.Equal(t, 5, runtimeconfig.Int("smtp_outbound_payload_ttl_minutes", 0, 1))
	require.Len(t, strings.Split(runtimeconfig.String("microsoft_domain_whitelist", ""), ","), 32)

	var count int64
	require.NoError(t, db.WithContext(context.Background()).Model(&infra.SettingModel{}).Count(&count).Error)
	require.Equal(t, int64(runtimeconfig.DefaultSettingsCount), count)
}
