package smsbower

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var smsbowerMigrationMySQL = testmysql.New("remail_smsbower_migration")

func TestSMSBowerMigrationMySQL(t *testing.T) {
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

	model := orderModel{
		OrderNo: "MYSQL-GMAIL-ORDER", ProjectID: 1, ProductID: 1, ServiceCode: "svc",
		Status: StatusPending, CodesJSON: "[]", UpstreamPriceSnapshot: "1",
		PointsPerUnitSnapshot: "1", CostPointsSnapshot: "1", MaxPriceSnapshot: "1", Version: 1,
	}
	require.NoError(t, db.Connection(func(tx *gorm.DB) error {
		if err := tx.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
			return err
		}
		defer tx.Exec("SET FOREIGN_KEY_CHECKS = 1")
		return tx.Create(&model).Error
	}))
	var stored orderModel
	require.NoError(t, db.Where("order_no = ?", model.OrderNo).Take(&stored).Error)
	require.JSONEq(t, "[]", stored.CodesJSON)

	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, through79, 78))
	var restored string
	require.NoError(t, db.Table("system_settings").Where("`key` = ?", "smsbower_code_enabled").Pluck("value", &restored).Error)
	require.Equal(t, "false", restored)
}
