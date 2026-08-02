package gmail

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var gmailMigrationMySQL = testmysql.New("remail_gmail_migration")

func TestGmailUpstreamMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrations := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	db := gmailMigrationMySQL.Database(t, migrations)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 70, version)
	require.True(t, db.Migrator().HasTable("gmail_resources"))
	require.True(t, db.Migrator().HasColumn("orders", "gmail_resource_id"))
	require.True(t, db.Migrator().HasColumn("orders", "gmail_cost_points_snapshot"))
	var allocationShape string
	require.NoError(t, db.Raw(`SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'chk_orders_allocation_shape'`).Scan(&allocationShape).Error)
	require.Contains(t, allocationShape, "gmail_resource_id")

	require.NoError(t, db.Exec(`INSERT INTO projects(id, name, target_platform, status) VALUES (990069, 'Gmail migration', 'gmail', 'listed')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(
id, project_id, type, status, code_enabled, purchase_enabled, code_price, purchase_price,
code_supplier_price, purchase_supplier_price, code_window_minutes, activation_window_minutes,
warranty_minutes, main_weight, dot_weight, plus_weight
	) VALUES (990069, 990069, 'gmail', 'enabled', 1, 1, 8, 12, 5, 7, 1440, 60, 1440, 1, 0, 0)`).Error)
	require.Error(t, db.Exec(`UPDATE project_products SET code_window_minutes = 10 WHERE id = 990069`).Error)
	require.NoError(t, db.Exec(`INSERT INTO smsbower_services(code, name, gmail_price, gmail_stock, last_seen_at) VALUES ('gm', 'Gmail', 1.2, 10, NOW(3))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_supply_routes(project_id, source, provider_service_code, code_enabled, purchase_enabled) VALUES (990069, 'smsbower', 'gm', 1, 0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_supply_routes(project_id, source, provider_service_code, code_enabled, purchase_enabled) VALUES (990069, 'local', '', 0, 1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_resources(email, identity, password, two_factor_secret, app_password)
VALUES ('first.last@gmail.com', 'firstlast@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop')`).Error)
	require.Error(t, db.Exec(`INSERT INTO gmail_resources(email, identity, password, two_factor_secret, app_password)
VALUES ('firstlast@googlemail.com', 'firstlast@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop')`).Error)
	require.Error(t, goose.DownTo(sqlDB, migrations, 69))
	require.NoError(t, db.Exec("DELETE FROM gmail_resources").Error)
	require.NoError(t, goose.DownTo(sqlDB, migrations, 69))
	require.False(t, db.Migrator().HasTable("gmail_resources"))
	require.False(t, db.Migrator().HasColumn("orders", "gmail_resource_id"))
	require.NoError(t, goose.UpTo(sqlDB, migrations, 70))

	require.NoError(t, db.Exec("DELETE FROM gmail_supply_routes WHERE project_id = 990069").Error)
	require.NoError(t, db.Exec("DELETE FROM project_products WHERE project_id = 990069").Error)
	require.NoError(t, db.Exec("DELETE FROM projects WHERE id = 990069").Error)
	require.NoError(t, db.Exec("DELETE FROM smsbower_services WHERE code = 'gm'").Error)
	require.NoError(t, goose.DownTo(sqlDB, migrations, 68))
	require.NoError(t, goose.UpTo(sqlDB, migrations, 70))
}
