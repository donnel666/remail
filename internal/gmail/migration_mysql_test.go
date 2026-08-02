package gmail

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

var gmailMigrationMySQL = testmysql.New("remail_gmail_migration")

func TestGmailMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrations := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	baselineMigrations := testmysql.MigrationsThrough(t, migrations, 71)
	gmailMigrations := testmysql.MigrationsThrough(t, migrations, 72)
	db := gmailMigrationMySQL.Database(t, baselineMigrations)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, platform.RunMigrations(sqlDB, gmailMigrations))
	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 72, version)

	for _, table := range []string{
		"smsbower_account_state", "smsbower_services", "gmail_supply_routes",
		"gmail_code_sessions", "gmail_resources", "gmail_allocations",
	} {
		require.True(t, db.Migrator().HasTable(table), table)
	}
	for _, column := range []string{"gmail_session_id", "gmail_resource_id", "gmail_cost_points_snapshot"} {
		require.False(t, db.Migrator().HasColumn("orders", column), column)
	}
	var allocationShape string
	require.NoError(t, db.Raw(`SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'chk_orders_allocation_shape'`).Scan(&allocationShape).Error)
	require.Contains(t, allocationShape, "gmail")
	require.NotContains(t, allocationShape, "gmail_session_id")
	require.NotContains(t, allocationShape, "gmail_resource_id")

	require.NoError(t, db.Exec(`INSERT INTO users(id, email, password_hash, nickname, role, user_group_id)
VALUES (990072, 'gmail-migration-owner@example.com', 'disabled', 'Gmail migration owner', 'super_admin', 1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO projects(id, name, target_platform, status)
VALUES (990069, 'Gmail migration', 'gmail', 'listed')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(
id, project_id, type, status, code_enabled, purchase_enabled, code_price, purchase_price,
code_supplier_price, purchase_supplier_price, code_window_minutes, activation_window_minutes,
warranty_minutes, main_weight, dot_weight, plus_weight
) VALUES (990069, 990069, 'gmail', 'enabled', 1, 1, 8, 12, 5, 7, 1440, 60, 1440, 1, 0, 0)`).Error)
	require.Error(t, db.Exec("UPDATE project_products SET code_window_minutes = 10 WHERE id = 990069").Error)
	require.NoError(t, db.Exec(`INSERT INTO smsbower_services(code, name, gmail_price, gmail_stock, last_seen_at)
VALUES ('gm', 'Gmail', 1.2, 10, NOW(3))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_supply_routes(project_id, source, provider_service_code, code_enabled, purchase_enabled)
VALUES (990069, 'smsbower', 'gm', 1, 0), (990069, 'local', '', 0, 1)`).Error)
	imported, err := NewService(db, nil).ImportLocalResources(context.Background(), 990072,
		"first.last@gmail.com----password----JBSWY3DPEHPK3PXP----abcdefghijklmnop", "abort")
	require.NoError(t, err)
	require.Equal(t, 1, imported.Imported)
	var gmailResourceID uint
	require.NoError(t, db.Table("gmail_resources").Where("identity = ?", "firstlast@gmail.com").Pluck("id", &gmailResourceID).Error)
	require.NotZero(t, gmailResourceID)
	var rootCount int64
	require.NoError(t, db.Table("email_resources").Where("id = ? AND type = 'gmail' AND owner_user_id = ?", gmailResourceID, 990072).Count(&rootCount).Error)
	require.EqualValues(t, 1, rootCount)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id) VALUES (990074, 'gmail', 990072)`).Error)
	require.Error(t, db.Exec(`INSERT INTO gmail_resources(id, resource_type, owner_user_id, email, identity, password, two_factor_secret, app_password)
VALUES (990074, 'gmail', 990072, 'firstlast@googlemail.com', 'firstlast@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_allocations(order_no, source, service_mode, resource_id, email, cost_points_snapshot)
VALUES ('GMAIL-MIGRATION-1', 'local', 'purchase', ?, 'first.last@gmail.com', 7)`, gmailResourceID).Error)
	require.Error(t, goose.DownTo(sqlDB, gmailMigrations, 71))

	require.NoError(t, db.Exec("DELETE FROM gmail_allocations").Error)
	require.NoError(t, db.Exec("DELETE FROM gmail_resources").Error)
	require.NoError(t, db.Exec("DELETE FROM email_resources WHERE type = 'gmail'").Error)
	require.NoError(t, db.Exec("DELETE FROM gmail_supply_routes WHERE project_id = 990069").Error)
	require.NoError(t, db.Exec("DELETE FROM project_products WHERE project_id = 990069").Error)
	require.NoError(t, db.Exec("DELETE FROM projects WHERE id = 990069").Error)
	require.NoError(t, goose.DownTo(sqlDB, gmailMigrations, 71))
	require.False(t, db.Migrator().HasTable("gmail_resources"))
	require.False(t, db.Migrator().HasTable("gmail_allocations"))
	require.False(t, db.Migrator().HasColumn("orders", "gmail_session_id"))
	require.NoError(t, db.Exec("DELETE FROM users WHERE id = 990072").Error)
	require.NoError(t, goose.UpTo(sqlDB, gmailMigrations, 72))
}
