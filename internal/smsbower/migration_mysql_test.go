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

func TestSMSBowerPublicErrorRedactionMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	through78 := testmysql.MigrationsThrough(t, source, 78)
	through95 := testmysql.MigrationsThrough(t, source, 95)
	through96 := testmysql.MigrationsThrough(t, source, 96)
	db := smsbowerMigrationMySQL.Database(t, through78)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, platform.RunMigrations(sqlDB, through95))

	legacy := "SMSBower 加载失败: Response status code does not indicate success: 401 Unauthorized"
	require.NoError(t, db.Connection(func(tx *gorm.DB) error {
		if err := tx.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
			return err
		}
		defer tx.Exec("SET FOREIGN_KEY_CHECKS = 1")
		if err := tx.Exec(`INSERT INTO smsbower_orders(
order_no, project_id, product_id, service_code, remote_mail_id, status, codes_json,
upstream_price_snapshot, points_per_unit_snapshot, cost_points_snapshot, max_price_snapshot,
last_safe_error, version
) VALUES
('SMS-PROVISION', 1, 1, 'svc', NULL, 'failed', '[]', 0, 1, 0, 0, ?, 1),
('SMS-CODE', 1, 1, 'svc', 1001, 'active', '[]', 0, 1, 0, 0, ?, 1),
('SMS-COMPLETED', 1, 1, 'svc', 1002, 'completed', '[]', 0, 1, 0, 0, ?, 1),
('SMS-PENDING-CANCEL', 1, 1, 'svc', NULL, 'cancelled', '[]', 0, 1, 0, 0, ?, 1),
('SMS-REMOTE-CANCEL', 1, 1, 'svc', 1003, 'cancelled', '[]', 0, 1, 0, 0, ?, 1)`,
			legacy, legacy, legacy, legacy, legacy).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO order_events(event_no, order_no, event_type, operator_type, reason) VALUES
('EVENT-SMS-FAILED', 'SMS-PROVISION', 'order.failed', 'system', ?),
('EVENT-SMS-REFUNDED', 'SMS-CODE', 'order.refunded', 'system', ?),
('EVENT-SMS-COMPLETED', 'SMS-COMPLETED', 'order.completed', 'system', 'SMSBower 接码生命周期已结束，共接收 1 个验证码。'),
('EVENT-SMS-ADMIN', 'SMS-CODE', 'order.refunded', 'admin', 'SMSBower administrator note'),
('EVENT-SMS-OTHER', 'SMS-CODE', 'order.closed', 'system', 'SMSBower internal close note'),
('EVENT-OTHER-PROVIDER', 'OTHER-PROVIDER', 'order.failed', 'system', ?)`, legacy, legacy, legacy).Error; err != nil {
			return err
		}
		return tx.Table("smsbower_account_state").Where("id = 1").Update("last_safe_error", legacy).Error
	}))

	require.NoError(t, platform.RunMigrations(sqlDB, through96))

	var eventRows []struct {
		EventNo string `gorm:"column:event_no"`
		Reason  string `gorm:"column:reason"`
	}
	require.NoError(t, db.Table("order_events").Select("event_no, reason").
		Where("event_no LIKE 'EVENT-%'").Find(&eventRows).Error)
	events := make(map[string]string, len(eventRows))
	for _, row := range eventRows {
		events[row.EventNo] = row.Reason
	}
	require.Equal(t, orderFailureReason, events["EVENT-SMS-FAILED"])
	require.Equal(t, codeFailureReason, events["EVENT-SMS-REFUNDED"])
	require.Equal(t, "接码服务已结束，共接收 1 个验证码。", events["EVENT-SMS-COMPLETED"])
	require.Equal(t, "SMSBower administrator note", events["EVENT-SMS-ADMIN"])
	require.Equal(t, "SMSBower internal close note", events["EVENT-SMS-OTHER"])
	require.Equal(t, legacy, events["EVENT-OTHER-PROVIDER"])

	var orderRows []struct {
		OrderNo       string `gorm:"column:order_no"`
		LastSafeError string `gorm:"column:last_safe_error"`
	}
	require.NoError(t, db.Table("smsbower_orders").Select("order_no, last_safe_error").
		Where("order_no LIKE 'SMS-%'").Find(&orderRows).Error)
	orders := make(map[string]string, len(orderRows))
	for _, row := range orderRows {
		orders[row.OrderNo] = row.LastSafeError
	}
	require.Equal(t, orderFailureReason, orders["SMS-PROVISION"])
	require.Equal(t, codeFailureReason, orders["SMS-CODE"])
	require.Empty(t, orders["SMS-COMPLETED"])
	require.Empty(t, orders["SMS-PENDING-CANCEL"])
	require.Empty(t, orders["SMS-REMOTE-CANCEL"])

	var accountError string
	require.NoError(t, db.Table("smsbower_account_state").Where("id = 1").Pluck("last_safe_error", &accountError).Error)
	require.Equal(t, legacy, accountError)

	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, through96, 95))
	var redacted string
	require.NoError(t, db.Table("order_events").Where("event_no = ?", "EVENT-SMS-FAILED").Pluck("reason", &redacted).Error)
	require.Equal(t, orderFailureReason, redacted)
}
