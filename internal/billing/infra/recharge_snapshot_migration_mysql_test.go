package infra

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var rechargeSnapshotMigrationMySQL = testmysql.New("remail_recharge_snapshot_migration")

func TestRechargeSnapshotQuarantineMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
	through127 := testmysql.MigrationsThrough(t, source, 127)
	through128 := testmysql.MigrationsThrough(t, source, 128)
	db := rechargeSnapshotMigrationMySQL.Database(t, through127)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	goose.SetTableName("goose_db_version")

	// These rows model legacy data and deliberately bypass the user foreign key.
	require.NoError(t, db.Exec("SET FOREIGN_KEY_CHECKS = 0").Error)
	require.NoError(t, db.Exec(`
INSERT INTO recharges(
    id, recharge_no, user_id, payment_method, recharge_quota, payment_amount,
    status, query_lease_until, gateway_config_snapshot
) VALUES
    (9801, 'RC-SNAPSHOT-NULL',    9801, 'alipay', 10, 10, 'paying',     '2026-08-25 00:10:00', NULL),
    (9802, 'RC-SNAPSHOT-BLANK',   9802, 'alipay', 10, 10, 'callback',   NULL,                    '   '),
    (9803, 'RC-SNAPSHOT-BAD',     9803, 'alipay', 10, 10, 'reconciled', NULL,                    '{not-json}'),
    (9804, 'RC-SNAPSHOT-EMPTY',   9804, 'alipay', 10, 10, 'paying',     NULL,                    '{}'),
    (9805, 'RC-SNAPSHOT-NO-GATEWAY-OBJECT', 9805, 'alipay', 10, 10, 'paying', NULL, '{"Enabled":true}'),
    (9806, 'RC-SNAPSHOT-NO-GATEWAY-ARRAY',  9806, 'alipay', 10, 10, 'callback', NULL, '[]'),
    (9807, 'RC-SNAPSHOT-NO-GATEWAY-SCALAR', 9807, 'alipay', 10, 10, 'reconciled', NULL, '1'),
    (9808, 'RC-SNAPSHOT-NO-GATEWAY-NULL',   9808, 'alipay', 10, 10, 'paying', NULL, '{"GatewayURL":null,"EpusdtGatewayURL":"   "}'),
    (9809, 'RC-SNAPSHOT-VALID',   9809, 'alipay', 10, 10, 'paying',     NULL,                    '{"GatewayURL":"https://pay.example.com"}'),
    (9810, 'RC-SNAPSHOT-CREDITED',9810, 'alipay', 10, 10, 'credited',   NULL,                    NULL),
    (9811, 'RC-SNAPSHOT-FAILED',  9811, 'alipay', 10, 10, 'failed',     NULL,                    NULL)`).Error)
	require.NoError(t, db.Exec("SET FOREIGN_KEY_CHECKS = 1").Error)

	require.NoError(t, goose.UpTo(sqlDB, through128, 128))

	type row struct {
		RechargeNo   string  `gorm:"column:recharge_no"`
		Status       string  `gorm:"column:status"`
		Failure      string  `gorm:"column:failure_reason"`
		Lease        *string `gorm:"column:query_lease_until"`
		ReconciledAt *string `gorm:"column:reconciled_at"`
	}
	var rows []row
	require.NoError(t, db.Table("recharges").
		Select("recharge_no, status, failure_reason, query_lease_until, reconciled_at").
		Where("recharge_no LIKE 'RC-SNAPSHOT-%'").Order("id").Find(&rows).Error)
	require.Len(t, rows, 11)

	for _, item := range rows[:8] {
		require.Equal(t, "failed", item.Status, item.RechargeNo)
		require.Equal(t, "migration_missing_gateway_snapshot", item.Failure, item.RechargeNo)
		require.Nil(t, item.Lease, item.RechargeNo)
		require.NotNil(t, item.ReconciledAt, item.RechargeNo)
	}
	require.Equal(t, "paying", rows[8].Status)
	require.Empty(t, rows[8].Failure)
	require.Equal(t, "credited", rows[9].Status)
	require.Equal(t, "failed", rows[10].Status)

	// A down migration must not silently reactivate an order whose original
	// provider configuration is unknowable.
	require.NoError(t, goose.DownTo(sqlDB, through128, 127))
	var status string
	require.NoError(t, db.Table("recharges").Where("recharge_no = ?", "RC-SNAPSHOT-NULL").Pluck("status", &status).Error)
	require.Equal(t, "failed", status)
}
