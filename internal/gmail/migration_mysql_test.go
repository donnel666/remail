package gmail

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var gmailMigrationMySQL = testmysql.New("remail_gmail_migration")

func TestGmailMigrationMySQL(t *testing.T) {
	db, sqlDB, migrations := newGmailMigrationDB(t, 77)
	requireMigrationVersion(t, sqlDB, 77)

	for _, table := range []string{
		"smsbower_account_state", "smsbower_services", "gmail_supply_routes",
		"gmail_code_sessions", "gmail_resources", "gmail_allocations",
	} {
		require.True(t, db.Migrator().HasTable(table), table)
	}
	for _, column := range []string{"gmail_session_id", "gmail_resource_id", "gmail_cost_points_snapshot"} {
		require.False(t, db.Migrator().HasColumn("orders", column), column)
	}
	allocationShape := migrationCheckClause(t, db, "orders", "chk_orders_allocation_shape")
	require.Contains(t, allocationShape, "gmail")
	require.NotContains(t, allocationShape, "gmail_session_id")
	require.NotContains(t, allocationShape, "gmail_resource_id")
	for _, column := range []string{
		"guard_type", "project_id", "product_id", "provider_cursor", "provider_spam_cursor", "supply_scope", "mailbox", "status", "released_at",
		"active_main_resource_id", "active_alias_mailbox", "active_alias_project_id", "active_alias_email",
	} {
		require.True(t, db.Migrator().HasColumn("gmail_allocations", column), column)
	}
	require.False(t, db.Migrator().HasColumn("gmail_allocations", "active_resource_id"))
	for _, column := range []string{"service_mode", "provider_cursor", "provider_spam_cursor"} {
		require.True(t, db.Migrator().HasColumn("gmail_code_sessions", column), column)
	}
	for _, column := range []string{
		"for_sale", "alloc_bucket", "last_allocated_at", "credential_revision", "credential_updated_at",
		"validation_generation", "validation_failures", "validation_request_id", "validation_command_hash",
	} {
		require.True(t, db.Migrator().HasColumn("gmail_resources", column), column)
	}
	for _, index := range []string{
		"idx_gmail_resources_alloc_public", "idx_gmail_resources_alloc_owned", "idx_gmail_resources_validation_pending",
	} {
		require.True(t, migrationIndexExists(t, db, "gmail_resources", index), index)
	}
	require.Contains(t, migrationCheckClause(t, db, "allocation_order_guards", "chk_allocation_order_guards_type"), "gmail")
	require.Equal(t, "NO", migrationConstraintEnforcement(t, db, "allocation_order_guards", "chk_allocation_order_guards_type"))
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_status"), "pending")
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_status"), "identifying")
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_status"), "available")
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_status"), "deleted")
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_validation_failures"), "3")
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_validation_generation"), "> 0")
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_credential_revision"), "> 0")
	require.Contains(t, migrationCheckClause(t, db, "gmail_allocations", "chk_gmail_allocations_mailbox"), "plus")
	for _, column := range []string{
		"gmail_history_scan_status", "gmail_history_scan_generation", "gmail_history_scan_failures",
		"gmail_history_scan_scanned_count", "gmail_history_scan_matched_count", "gmail_history_scan_skipped_count",
		"gmail_history_scan_request_id", "gmail_history_scan_last_safe_error", "gmail_history_scan_requested_at",
		"gmail_history_scan_started_at", "gmail_history_scan_finished_at",
	} {
		require.True(t, db.Migrator().HasColumn("projects", column), column)
	}
	require.True(t, migrationIndexExists(t, db, "projects", "idx_projects_gmail_history_scan_pending"))
	require.Contains(t, migrationCheckClause(t, db, "projects", "chk_projects_gmail_history_scan_status"), "processing")
	for _, index := range []string{
		"idx_gmail_allocations_active_main", "idx_gmail_allocations_active_alias",
		"idx_gmail_allocations_resource_project", "idx_gmail_allocations_project_mailbox_email",
	} {
		require.True(t, migrationIndexExists(t, db, "gmail_allocations", index), index)
	}
	require.Contains(t, migrationCheckClause(t, db, "mailmatch_messages", "chk_mailmatch_messages_resource_type"), "gmail")
	require.True(t, migrationColumnNullable(t, db, "gmail_allocations", "project_id"))
	require.True(t, migrationColumnNullable(t, db, "gmail_allocations", "product_id"))

	require.NoError(t, goose.DownTo(sqlDB, migrations, 76))
	requireMigrationVersion(t, sqlDB, 76)
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_status"), "deleted")
	require.NotContains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_status"), "identifying")
	require.NoError(t, platform.RunMigrations(sqlDB, migrations))
	requireMigrationVersion(t, sqlDB, 77)

	seedGmailMigrationProject(t, db)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id)
VALUES (990074, 'gmail', 990072)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_resources(
id, resource_type, owner_user_id, email, identity, password, two_factor_secret, app_password
) VALUES (990074, 'gmail', 990072, 'fresh@gmail.com', 'fresh@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop')`).Error)
	var status string
	require.NoError(t, db.Table("gmail_resources").Where("id = 990074").Pluck("status", &status).Error)
	require.Equal(t, localResourceRollbackNormal, status)
	var fresh struct {
		ForSale              bool   `gorm:"column:for_sale"`
		CredentialRevision   uint64 `gorm:"column:credential_revision"`
		ValidationGeneration uint64 `gorm:"column:validation_generation"`
	}
	require.NoError(t, db.Table("gmail_resources").Where("id = 990074").Take(&fresh).Error)
	require.False(t, fresh.ForSale)
	require.EqualValues(t, 1, fresh.CredentialRevision)
	require.EqualValues(t, 1, fresh.ValidationGeneration)
	require.NoError(t, db.Exec("UPDATE gmail_resources SET status = 'normal' WHERE id = 990074").Error)

	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('GMAIL-MIGRATION-1', 'gmail')").Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_allocations(
order_no, guard_type, project_id, product_id, source, source_ref, service_mode,
resource_id, supply_scope, mailbox, email, status, cost_points_snapshot
) VALUES ('GMAIL-MIGRATION-1', 'gmail', 990069, 990069, 'local', '', 'code',
990074, 'public', 'main', 'fresh@gmail.com', 'allocated', 7)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_allocations(
order_no, guard_type, project_id, product_id, source, source_ref, service_mode,
resource_id, supply_scope, mailbox, email, status, cost_points_snapshot
) VALUES ('GMAIL-MIGRATION-2', 'gmail', 990069, 990069, 'local', '', 'code',
990074, 'public', 'dot', 'f.resh@gmail.com', 'allocated', 7)`).Error)
	require.Error(t, db.Exec(`INSERT INTO gmail_allocations(
order_no, guard_type, project_id, product_id, source, source_ref, service_mode,
resource_id, supply_scope, mailbox, email, status, cost_points_snapshot
) VALUES ('GMAIL-MIGRATION-3', 'gmail', 990069, 990069, 'local', '', 'code',
990074, 'public', 'dot', 'f.resh@gmail.com', 'allocated', 7)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_allocations(
order_no, guard_type, project_id, product_id, source, source_ref, service_mode,
resource_id, supply_scope, mailbox, email, status, cost_points_snapshot
) VALUES ('GMAIL-MIGRATION-3', 'gmail', 990069, 990069, 'local', '', 'code',
990074, 'public', 'plus', 'fresh+legacy@gmail.com', 'allocated', 7)`).Error)
	require.NoError(t, db.Exec("UPDATE gmail_allocations SET status = 'released', released_at = NOW(3) WHERE order_no = 'GMAIL-MIGRATION-1'").Error)
	require.NoError(t, db.Exec("UPDATE gmail_resources SET status = 'sold' WHERE id = 990074").Error)
	// The previous image does not send any of the version-77 columns and does
	// not create an allocation guard. Its INSERT must remain valid for rollback.
	require.NoError(t, db.Exec(`INSERT INTO gmail_allocations(
order_no, source, source_ref, service_mode, resource_id, email, cost_points_snapshot
) VALUES ('GMAIL-MIGRATION-4', 'local', '', 'purchase', 990074, 'fresh@gmail.com', 7)`).Error)
	var oldInsert struct {
		ProjectID *uint `gorm:"column:project_id"`
		ProductID *uint `gorm:"column:product_id"`
	}
	require.NoError(t, db.Table("gmail_allocations").Where("order_no = 'GMAIL-MIGRATION-4'").Take(&oldInsert).Error)
	require.Nil(t, oldInsert.ProjectID)
	require.Nil(t, oldInsert.ProductID)
	var oldInsertGuardCount int64
	require.NoError(t, db.Table("allocation_order_guards").Where("order_no = 'GMAIL-MIGRATION-4'").Count(&oldInsertGuardCount).Error)
	require.Zero(t, oldInsertGuardCount)

	require.NoError(t, db.Exec(`INSERT INTO mailmatch_messages(
email_resource_id, resource_type, recipient, dedupe_key, received_at
) VALUES (990074, 'gmail', 'fresh@gmail.com', REPEAT('a', 64), NOW())`).Error)

	// Version 77 refuses a lossy rollback once released allocations or Gmail
	// mailmatch messages exist; the older Gmail migration remains untouched.
	require.Error(t, goose.DownTo(sqlDB, migrations, 76))
}

func TestGmailMaintenanceMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	baseline := testmysql.MigrationsThrough(t, source, 71)
	through87 := testmysql.MigrationsThrough(t, source, 87)
	through88 := testmysql.MigrationsThrough(t, source, 88)
	db := gmailMigrationMySQL.Database(t, baseline)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, platform.RunMigrations(sqlDB, through87))
	requireMigrationVersion(t, sqlDB, 87)
	require.NoError(t, platform.RunMigrations(sqlDB, through88))
	requireMigrationVersion(t, sqlDB, 88)
	require.True(t, db.Migrator().HasTable("gmail_maintenance_runs"))
	for _, index := range []string{
		"idx_gmail_maintenance_resource_updated",
		"idx_gmail_maintenance_resource_generation",
		"idx_gmail_maintenance_dispatch",
	} {
		require.True(t, migrationIndexExists(t, db, "gmail_maintenance_runs", index), index)
	}
}

func TestGmailDeletedRestoreMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	through74 := testmysql.MigrationsThrough(t, source, 74)
	through76 := testmysql.MigrationsThrough(t, source, 76)
	db := gmailMigrationMySQL.Database(t, through74)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, platform.RunMigrations(sqlDB, through76))
	requireMigrationVersion(t, sqlDB, 76)
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_status"), "deleted")

	var updateRule string
	require.NoError(t, db.Raw(`SELECT update_rule
FROM information_schema.REFERENTIAL_CONSTRAINTS
WHERE constraint_schema = DATABASE()
  AND table_name = 'gmail_resources'
  AND constraint_name = 'fk_gmail_resources_root_owner'`).Scan(&updateRule).Error)
	require.Equal(t, "CASCADE", updateRule)
}

func TestGmailMigrationBackfillAndResumeMySQL(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "legacy"
		if partial {
			name = "partial_ddl"
		}
		t.Run(name, func(t *testing.T) {
			db, sqlDB, migrations := newGmailMigrationDB(t, 76)
			seedLegacyGmailMigrationRows(t, db)
			if partial {
				applyPartialGmailMigrationDDL(t, db)
			}

			require.NoError(t, platform.RunMigrations(sqlDB, migrations))
			requireMigrationVersion(t, sqlDB, 77)
			assertLegacyGmailMigrationRows(t, db)

			if partial {
				require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id = 77").Error)
				require.NoError(t, platform.RunMigrations(sqlDB, migrations))
				requireMigrationVersion(t, sqlDB, 77)
				assertLegacyGmailMigrationRows(t, db)
			}
		})
	}
}

func TestGmailAppPasswordOnlyMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	through131 := testmysql.MigrationsThrough(t, source, 131)
	through132 := testmysql.MigrationsThrough(t, source, 132)
	db := gmailMigrationMySQL.Database(t, through131)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	requireMigrationVersion(t, sqlDB, 131)

	seedGmailMigrationProject(t, db)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id, type, owner_user_id) VALUES (990074, 'gmail', 990072)").Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_resources(
id, resource_type, owner_user_id, email, identity, password, two_factor_secret, app_password, status
) VALUES (990074, 'gmail', 990072, 'app-only@gmail.com', 'app-only@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop', 'normal')`).Error)

	require.NoError(t, platform.RunMigrations(sqlDB, through132))
	requireMigrationVersion(t, sqlDB, 132)
	clause := migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_credentials")
	require.Contains(t, clause, "app_password")
	require.NotContains(t, clause, "`password`")
	require.NoError(t, db.Exec("UPDATE gmail_resources SET password = '', two_factor_secret = '' WHERE id = 990074").Error)

	require.Error(t, goose.DownTo(sqlDB, through132, 131), "rollback must reject APP-password-only rows")
	requireMigrationVersion(t, sqlDB, 132)
	require.NoError(t, db.Exec("UPDATE gmail_resources SET password = 'password', two_factor_secret = 'JBSWY3DPEHPK3PXP' WHERE id = 990074").Error)
	require.NoError(t, goose.DownTo(sqlDB, through132, 131))
	requireMigrationVersion(t, sqlDB, 131)
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_credentials"), "`password`")
}

func newGmailMigrationDB(t *testing.T, version int) (*gorm.DB, *sql.DB, string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	baseline := testmysql.MigrationsThrough(t, source, 71)
	currentMigrations := testmysql.MigrationsThrough(t, source, version)
	migrations := testmysql.MigrationsThrough(t, source, 77)
	db := gmailMigrationMySQL.Database(t, baseline)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	if version > 71 {
		require.NoError(t, platform.RunMigrations(sqlDB, currentMigrations))
	}
	return db, sqlDB, migrations
}

func requireMigrationVersion(t *testing.T, db *sql.DB, want int64) {
	t.Helper()
	goose.SetTableName("goose_db_version")
	require.NoError(t, goose.SetDialect("mysql"))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.EqualValues(t, want, version)
}

func migrationCheckClause(t *testing.T, db *gorm.DB, table, constraint string) string {
	t.Helper()
	var clause string
	require.NoError(t, db.Raw(`SELECT cc.CHECK_CLAUSE
FROM information_schema.TABLE_CONSTRAINTS AS tc
JOIN information_schema.CHECK_CONSTRAINTS AS cc
  ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
 AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
  AND tc.TABLE_NAME = ?
  AND tc.CONSTRAINT_NAME = ?`, table, constraint).Scan(&clause).Error)
	require.NotEmpty(t, clause)
	return clause
}

func migrationConstraintEnforcement(t *testing.T, db *gorm.DB, table, constraint string) string {
	t.Helper()
	var enforced string
	require.NoError(t, db.Raw(`SELECT enforced
FROM information_schema.TABLE_CONSTRAINTS
WHERE constraint_schema = DATABASE()
  AND table_name = ?
  AND constraint_name = ?`, table, constraint).Scan(&enforced).Error)
	require.NotEmpty(t, enforced)
	return enforced
}

func migrationColumnNullable(t *testing.T, db *gorm.DB, table, column string) bool {
	t.Helper()
	var nullable string
	require.NoError(t, db.Raw(`SELECT is_nullable
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND column_name = ?`, table, column).Scan(&nullable).Error)
	require.NotEmpty(t, nullable)
	return nullable == "YES"
}

func migrationIndexExists(t *testing.T, db *gorm.DB, table, index string) bool {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*)
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND index_name = ?`, table, index).Scan(&count).Error)
	return count > 0
}

func seedGmailMigrationProject(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO users(id, email, password_hash, nickname, role, user_group_id)
VALUES (990072, 'gmail-migration-owner@example.com', 'disabled', 'Gmail migration owner', 'super_admin', 1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO projects(id, name, target_platform, status)
VALUES (990069, 'Gmail migration', 'gmail', 'listed')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(
id, project_id, type, status, code_enabled, purchase_enabled, code_price, purchase_price,
code_supplier_price, purchase_supplier_price, code_window_minutes, activation_window_minutes,
warranty_minutes, main_weight, dot_weight, plus_weight
) VALUES (990069, 990069, 'gmail', 'enabled', 1, 1, 8, 12, 5, 7, 1440, 60, 1440, 1, 0, 0)`).Error)
	for i, orderNo := range []string{"GMAIL-MIGRATION-1", "GMAIL-MIGRATION-2", "GMAIL-MIGRATION-3", "GMAIL-MIGRATION-4"} {
		require.NoError(t, db.Exec(`INSERT INTO orders(
order_no, user_id, project_id, project_product_id, product_type, service_mode,
status, pay_amount, client_channel, idempotency_key, request_fingerprint
) VALUES (?, 990072, 990069, 990069, 'gmail', 'code',
'pending_payment', 8, 'console', ?, REPEAT(?, 64))`, orderNo, orderNo, string(rune('a'+i))).Error)
	}
}

func seedLegacyGmailMigrationRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	seedGmailMigrationProject(t, db)
	require.NoError(t, db.Exec(`INSERT INTO smsbower_services(code, name, gmail_price, gmail_stock, last_seen_at)
VALUES ('gm', 'Gmail', 1.2, 10, NOW(3))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_supply_routes(project_id, source, provider_service_code, code_enabled, purchase_enabled)
VALUES (990069, 'smsbower', 'gm', 1, 0), (990069, 'local', '', 1, 1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_resources(id, type, owner_user_id) VALUES
(990074, 'gmail', 990072), (990075, 'gmail', 990072),
(990076, 'gmail', 990072), (990077, 'gmail', 990072),
(990078, 'gmail', 990072)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_resources(
id, resource_type, owner_user_id, email, identity, password, two_factor_secret, app_password, status
) VALUES
(990074, 'gmail', 990072, 'available@gmail.com', 'available@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop', 'available'),
(990075, 'gmail', 990072, 'leased@gmail.com', 'leased@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop', 'leased'),
(990076, 'gmail', 990072, 'sold@gmail.com', 'sold@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop', 'sold'),
(990077, 'gmail', 990072, 'disabled@gmail.com', 'disabled@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop', 'disabled'),
(990078, 'gmail', 990072, 'validating@gmail.com', 'validating@gmail.com', 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop', 'validating')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_allocations(
order_no, source, source_ref, service_mode, resource_id, email, cost_points_snapshot
) VALUES ('GMAIL-MIGRATION-1', 'local', '', 'code', 990075, 'leased@gmail.com', 7)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gmail_code_sessions(
order_no, source, source_ref, provider_service_code, email, status, codes_json
) VALUES ('GMAIL-MIGRATION-1', 'smsbower', 'mail-1', 'gm', 'leased@gmail.com', 'active', JSON_ARRAY())`).Error)
}

func applyPartialGmailMigrationDDL(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`ALTER TABLE allocation_order_guards
DROP CHECK chk_allocation_order_guards_type,
ADD CONSTRAINT chk_allocation_order_guards_type CHECK (type IN ('microsoft', 'domain', 'gmail'))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no, type)
SELECT order_no, 'gmail' FROM gmail_allocations`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE gmail_resources DROP CHECK chk_gmail_resources_status`).Error)
	require.NoError(t, db.Exec(`UPDATE gmail_resources SET status = 'normal' WHERE status = 'available'`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE gmail_resources
ADD COLUMN for_sale TINYINT(1) NOT NULL DEFAULT 0 AFTER app_password,
ADD COLUMN credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER app_password,
ADD COLUMN validation_generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER status,
ADD COLUMN validation_failures TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER validation_generation,
ADD COLUMN alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0 AFTER status,
ADD COLUMN last_allocated_at DATETIME(3) NULL AFTER alloc_bucket`).Error)
	require.NoError(t, db.Exec(`UPDATE gmail_resources SET for_sale = TRUE`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE gmail_resources
ADD INDEX idx_gmail_resources_alloc_public (alloc_bucket, for_sale, status, last_allocated_at, id),
ADD CONSTRAINT chk_gmail_resources_validation_failures CHECK (validation_failures <= 3)`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE gmail_code_sessions
ADD COLUMN service_mode VARCHAR(32) NULL AFTER provider_service_code`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE gmail_allocations
ADD COLUMN guard_type VARCHAR(32) NOT NULL DEFAULT 'gmail' AFTER order_no,
ADD COLUMN project_id BIGINT UNSIGNED NULL AFTER guard_type,
ADD COLUMN provider_cursor BIGINT UNSIGNED NULL AFTER source_ref`).Error)
}

func assertLegacyGmailMigrationRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	var statuses []string
	require.NoError(t, db.Table("gmail_resources").Order("id").Pluck("status", &statuses).Error)
	require.Equal(t, []string{"available", "leased", "abnormal", "disabled", "pending"}, statuses)

	var validationGeneration uint64
	require.NoError(t, db.Table("gmail_resources").Where("id = 990078").Pluck("validation_generation", &validationGeneration).Error)
	require.EqualValues(t, 2, validationGeneration)
	var publicRows int64
	require.NoError(t, db.Table("gmail_resources").Where("for_sale = TRUE").Count(&publicRows).Error)
	require.EqualValues(t, 5, publicRows)
	for _, column := range []string{
		"credential_updated_at", "validation_request_id", "validation_command_hash",
	} {
		require.True(t, db.Migrator().HasColumn("gmail_resources", column), column)
	}
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_validation_generation"), "> 0")
	require.Contains(t, migrationCheckClause(t, db, "gmail_resources", "chk_gmail_resources_credential_revision"), "> 0")
	var badBuckets int64
	require.NoError(t, db.Table("gmail_resources").Where("alloc_bucket <> MOD(id, 2048)").Count(&badBuckets).Error)
	require.Zero(t, badBuckets)
	for _, index := range []string{
		"idx_gmail_resources_alloc_public", "idx_gmail_resources_alloc_owned", "idx_gmail_resources_validation_pending",
	} {
		require.True(t, migrationIndexExists(t, db, "gmail_resources", index), index)
	}

	var allocation struct {
		GuardType          string `gorm:"column:guard_type"`
		ProjectID          uint   `gorm:"column:project_id"`
		ProductID          uint   `gorm:"column:product_id"`
		ProviderCursor     uint64 `gorm:"column:provider_cursor"`
		ProviderSpamCursor uint64 `gorm:"column:provider_spam_cursor"`
		SupplyScope        string `gorm:"column:supply_scope"`
		Mailbox            string `gorm:"column:mailbox"`
		Status             string `gorm:"column:status"`
	}
	require.NoError(t, db.Table("gmail_allocations").Where("order_no = 'GMAIL-MIGRATION-1'").Take(&allocation).Error)
	require.Equal(t, "gmail", allocation.GuardType)
	require.EqualValues(t, 990069, allocation.ProjectID)
	require.EqualValues(t, 990069, allocation.ProductID)
	require.Zero(t, allocation.ProviderCursor)
	require.Zero(t, allocation.ProviderSpamCursor)
	require.Equal(t, "public", allocation.SupplyScope)
	require.Equal(t, "main", allocation.Mailbox)
	require.Equal(t, "allocated", allocation.Status)

	var guardCount, localRouteCount int64
	require.NoError(t, db.Table("allocation_order_guards").Where("order_no = 'GMAIL-MIGRATION-1' AND type = 'gmail'").Count(&guardCount).Error)
	require.EqualValues(t, 1, guardCount)
	require.NoError(t, db.Table("gmail_supply_routes").Where("source = 'local'").Count(&localRouteCount).Error)
	require.EqualValues(t, 1, localRouteCount)

	var session struct {
		ServiceMode        string `gorm:"column:service_mode"`
		ProviderCursor     uint64 `gorm:"column:provider_cursor"`
		ProviderSpamCursor uint64 `gorm:"column:provider_spam_cursor"`
	}
	require.NoError(t, db.Table("gmail_code_sessions").Where("order_no = 'GMAIL-MIGRATION-1'").Take(&session).Error)
	require.Equal(t, "code", session.ServiceMode)
	require.Zero(t, session.ProviderCursor)
	require.Zero(t, session.ProviderSpamCursor)
	require.Contains(t, migrationCheckClause(t, db, "mailmatch_messages", "chk_mailmatch_messages_resource_type"), "gmail")
}
