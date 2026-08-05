package smsbower

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var smsbowerMigrationMySQL = testmysql.New("remail_smsbower_migration")

func TestSMSBowerMigrationPreservesDisabledCodeSwitch(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	through78 := testmysql.MigrationsThrough(t, source, 78)
	through79 := testmysql.MigrationsThrough(t, source, 79)
	db := smsbowerMigrationMySQL.Database(t, through78)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO system_settings(`+"`key`"+`, value) VALUES
		('smsbower_enabled', 'true'),
		('smsbower_code_enabled', 'false'),
		('smsbower_api_key', 'secret')
		ON DUPLICATE KEY UPDATE value = VALUES(value)`).Error)

	require.NoError(t, platform.RunMigrations(sqlDB, through79))
	var enabled bool
	require.NoError(t, db.Table("smsbower_config").Where("id = 1").Pluck("enabled", &enabled).Error)
	require.False(t, enabled)

	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, through79, 78))
	var restored string
	require.NoError(t, db.Table("system_settings").Where("`key` = ?", "smsbower_code_enabled").Pluck("value", &restored).Error)
	require.Equal(t, "false", restored)
}
