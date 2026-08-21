package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestRoutingCandidateTablesRemovedMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	migrationsDir := allocMigrationsDir(t)
	require.NoError(t, goose.SetDialect("mysql"))

	require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 83))
	require.True(t, db.Migrator().HasTable("microsoft_routing_candidates"))
	require.True(t, db.Migrator().HasTable("domain_routing_candidates"))

	var refreshColumns int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'projects'
  AND column_name LIKE 'candidate_refresh_%'`).Scan(&refreshColumns).Error)
	require.Equal(t, int64(11), refreshColumns)

	require.NoError(t, goose.UpTo(sqlDB, migrationsDir, 84))

	require.False(t, db.Migrator().HasTable("microsoft_routing_candidates"))
	require.False(t, db.Migrator().HasTable("domain_routing_candidates"))

	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'projects'
  AND column_name LIKE 'candidate_refresh_%'`).Scan(&refreshColumns).Error)
	require.Zero(t, refreshColumns)
}

func TestLegacyOrderAllocationIDMigrationResumesAfterManualDDLMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id >= 86").Error)

	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 85, version)
	require.NoError(t, goose.UpTo(sqlDB, allocMigrationsDir(t), 86))

	version, err = goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 86, version)

	var legacyObjects int64
	require.NoError(t, db.Raw(`
SELECT
    (SELECT COUNT(*) FROM information_schema.columns
      WHERE table_schema = DATABASE() AND table_name = 'orders'
        AND column_name IN ('microsoft_alloc_id', 'domain_alloc_id'))
  + (SELECT COUNT(*) FROM information_schema.table_constraints
      WHERE constraint_schema = DATABASE() AND table_name = 'orders'
        AND constraint_name IN ('fk_orders_ms_alloc', 'fk_orders_domain_alloc'))
  + (SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics
      WHERE table_schema = DATABASE() AND table_name = 'orders'
        AND index_name IN ('fk_orders_ms_alloc', 'fk_orders_domain_alloc'))`).Scan(&legacyObjects).Error)
	require.Zero(t, legacyObjects)
}

func TestLegacyOrderAllocationIDMigrationRejectsInconsistentLinkageMySQL(t *testing.T) {
	tests := []struct {
		name        string
		productType string
	}{
		{name: "microsoft", productType: "microsoft"},
		{name: "domain", productType: "domain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newAllocMySQLTestDB(t)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, goose.SetDialect("mysql"))
			migrationsDir := allocMigrationsDir(t)
			require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 85))

			mainWeight := 0
			if tt.productType == "microsoft" {
				mainWeight = 1
			}
			seedAllocBase(t, db, tt.productType, mainWeight, 0, 0)
			require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id,
    product_type, service_mode, supply_policy, status,
    pay_amount, refund_amount, allocation_type,
    delivery_email, client_channel,
    idempotency_key, request_fingerprint,
    service_cleanup_status
) VALUES (
    ?, 2, 10, 20,
    ?, 'code', 'private_first', 'pending_payment',
    1, 0, ?,
    '', 'console',
    ?, REPEAT('a', 64),
    'none'
)`, "MIG86-INCONSISTENT-"+tt.name, tt.productType, tt.productType, "mig86-inconsistent-"+tt.name).Error)

			err = goose.UpTo(sqlDB, migrationsDir, 86)
			require.ErrorContains(t, err, "inconsistent "+map[string]string{
				"microsoft": "Microsoft",
				"domain":    "Domain",
			}[tt.productType]+" order allocation linkage")

			version, err := goose.GetDBVersion(sqlDB)
			require.NoError(t, err)
			require.EqualValues(t, 85, version)

			var legacyObjects int64
			require.NoError(t, db.Raw(`
SELECT
    (SELECT COUNT(*) FROM information_schema.columns
      WHERE table_schema = DATABASE() AND table_name = 'orders'
        AND column_name IN ('microsoft_alloc_id', 'domain_alloc_id'))
  + (SELECT COUNT(*) FROM information_schema.table_constraints
      WHERE constraint_schema = DATABASE() AND table_name = 'orders'
        AND constraint_name IN ('fk_orders_ms_alloc', 'fk_orders_domain_alloc'))
  + (SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics
      WHERE table_schema = DATABASE() AND table_name = 'orders'
        AND index_name IN ('fk_orders_ms_alloc', 'fk_orders_domain_alloc'))`).Scan(&legacyObjects).Error)
			require.EqualValues(t, 6, legacyObjects)
		})
	}
}

func TestProjectScopedActiveMigrationRoundTripMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	migrationsDir := allocMigrationsDir(t)
	require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 120))

	var nullableColumns int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'gmail_allocations'
  AND column_name IN ('project_id', 'product_id')
  AND is_nullable = 'YES'`).Scan(&nullableColumns).Error)
	require.EqualValues(t, 2, nullableColumns)

	seedAllocBase(t, db, "gmail", 1, 0, 0)
	seedGmailResources(t, db, []gmailResourceSeed{{
		ID: 1000, OwnerUserID: 1, Email: "migration@gmail.com", ForSale: true,
	}})
	require.NoError(t, db.Exec(`
INSERT INTO orders(
    order_no, user_id, project_id, project_product_id, product_type,
    service_mode, status, pay_amount, client_channel,
    idempotency_key, request_fingerprint
) VALUES (
    'migration-121-backfill', 2, 10, 20, 'gmail',
    'code', 'pending_payment', 1, 'console',
    'migration-121-backfill', REPEAT('a', 64)
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO gmail_allocations(
    order_no, source, source_ref, service_mode, resource_id, email,
    cost_points_snapshot
) VALUES (
    'migration-121-backfill', 'local', '', 'code', 1000,
    'migration@gmail.com', 1
)`).Error)

	require.NoError(t, goose.UpTo(sqlDB, migrationsDir, 121))
	var ownership struct {
		ProjectID uint `gorm:"column:project_id"`
		ProductID uint `gorm:"column:product_id"`
	}
	require.NoError(t, db.Table("gmail_allocations").
		Where("order_no = 'migration-121-backfill'").Take(&ownership).Error)
	require.Equal(t, uint(10), ownership.ProjectID)
	require.Equal(t, uint(20), ownership.ProductID)

	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'gmail_allocations'
  AND column_name IN ('project_id', 'product_id')
  AND is_nullable = 'NO'`).Scan(&nullableColumns).Error)
	require.EqualValues(t, 2, nullableColumns)
	require.Error(t, db.Exec(`
INSERT INTO gmail_allocations(
    order_no, source, source_ref, service_mode, email, cost_points_snapshot
) VALUES (
    'migration-121-null-rejected', 'smsbower', 'remote-1', 'code',
    'remote@gmail.com', 1
)`).Error)

	require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 119))
	require.NoError(t, goose.UpTo(sqlDB, migrationsDir, 121))
	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 121, version)
}

func TestProjectScopedActiveMigrationResumesAfterManualDDLMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	migrationsDir := allocMigrationsDir(t)
	require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 119))

	require.NoError(t, db.Exec(`
ALTER TABLE gmail_allocations
    MODIFY COLUMN project_id BIGINT UNSIGNED NOT NULL,
    MODIFY COLUMN product_id BIGINT UNSIGNED NOT NULL,
    ALGORITHM=INPLACE,
    LOCK=NONE`).Error)
	require.NoError(t, db.Exec(`
ALTER TABLE gmail_allocations
    ADD UNIQUE INDEX idx_gmail_allocations_active_main_project
        (project_id, active_main_resource_id),
    ALGORITHM=INPLACE,
    LOCK=NONE`).Error)
	require.NoError(t, db.Exec(`
ALTER TABLE gmail_allocations
    DROP INDEX idx_gmail_allocations_active_main,
    ALGORITHM=INPLACE,
    LOCK=NONE`).Error)
	require.NoError(t, db.Exec(`
ALTER TABLE icloud_allocations
    DROP INDEX uk_icloud_allocations_active_alias,
    ALGORITHM=INPLACE,
    LOCK=NONE`).Error)
	require.NoError(t, db.Exec(`
ALTER TABLE microsoft_allocations
    ADD UNIQUE INDEX idx_ms_alloc_active_project
        (active_kind, project_id, active_entity_id),
    ADD INDEX idx_ms_alloc_active_legacy_lookup
        (active_kind, active_project_id, active_entity_id),
    ALGORITHM=INPLACE,
    LOCK=NONE`).Error)
	require.NoError(t, db.Exec(`
ALTER TABLE microsoft_allocations
    DROP INDEX idx_ms_alloc_active,
    ALGORITHM=INPLACE,
    LOCK=NONE`).Error)

	require.NoError(t, goose.UpTo(sqlDB, migrationsDir, 121))
	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 121, version)
	requireIndexExists(t, db, "gmail_allocations", "idx_gmail_allocations_active_main_project")
	requireIndexExists(t, db, "microsoft_allocations", "idx_ms_alloc_active_project")
	requireIndexExists(t, db, "microsoft_allocations", "idx_ms_alloc_active_legacy_lookup")
	requireIndexMissing(t, db, "gmail_allocations", "idx_gmail_allocations_active_main")
	requireIndexMissing(t, db, "icloud_allocations", "uk_icloud_allocations_active_alias")
	requireIndexMissing(t, db, "microsoft_allocations", "idx_ms_alloc_active")
}

func TestProjectScopedActiveMigrationDownRejectsCrossProjectMicrosoftAndGmailMySQL(t *testing.T) {
	tests := []struct {
		name        string
		productType string
		mailbox     string
	}{
		{name: "gmail main", productType: "gmail", mailbox: "main"},
		{name: "microsoft main", productType: "microsoft", mailbox: "main"},
		{name: "microsoft explicit alias", productType: "microsoft", mailbox: "alias"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newAllocMySQLTestDB(t)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, goose.SetDialect("mysql"))
			seedAllocBase(t, db, tt.productType, 1, 0, 0)
			require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type)
VALUES (11, 'Other Migration Project', 'alloc', 'listed', 'public')`).Error)
			require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (21, 11, ?, 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 1, 0, 0)`,
				tt.productType).Error)

			switch tt.productType {
			case "gmail":
				seedGmailResources(t, db, []gmailResourceSeed{{
					ID: 1000, OwnerUserID: 1, Email: "shared@gmail.com", ForSale: true,
				}})
				require.NoError(t, db.Exec(`
INSERT INTO gmail_allocations(
    order_no, project_id, product_id, source, service_mode,
    resource_id, supply_scope, mailbox, email
) VALUES
    ('migration-121-gmail-10', 10, 20, 'local', 'code', 1000, 'public', 'main', 'shared@gmail.com'),
    ('migration-121-gmail-11', 11, 21, 'local', 'code', 1000, 'public', 'main', 'shared@gmail.com')`).Error)
			case "microsoft":
				seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
				require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type) VALUES
    ('migration-121-ms-10', 'microsoft'),
    ('migration-121-ms-11', 'microsoft')`).Error)
				if tt.mailbox == "alias" {
					require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (1000, 1, 'shared-alias@example.com', 'normal')`).Error)
					var aliasID uint
					require.NoError(t, db.Table("explicit_aliases").Select("id").
						Where("resource_id = 1000").Scan(&aliasID).Error)
					require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope,
    mailbox, explicit_alias_id, email
) VALUES
    ('migration-121-ms-10', 10, 20, 1000, 'public', 'alias', ?, 'shared-alias@example.com'),
    ('migration-121-ms-11', 11, 21, 1000, 'public', 'alias', ?, 'shared-alias@example.com')`,
						aliasID, aliasID).Error)
				} else {
					require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox, email
) VALUES
    ('migration-121-ms-10', 10, 20, 1000, 'public', 'main', 'ms1000@example.com'),
    ('migration-121-ms-11', 11, 21, 1000, 'public', 'main', 'ms1000@example.com')`).Error)
				}
			}

			err = goose.DownTo(sqlDB, allocMigrationsDir(t), 120)
			require.ErrorContains(t, err, "chk_project_scoped_active_down_guard")
			version, versionErr := goose.GetDBVersion(sqlDB)
			require.NoError(t, versionErr)
			require.EqualValues(t, 121, version)

			var projects int64
			if tt.productType == "gmail" {
				require.NoError(t, db.Raw(`
SELECT COUNT(DISTINCT project_id)
FROM gmail_allocations
WHERE status = 'allocated' AND mailbox = 'main'`).Scan(&projects).Error)
			} else {
				require.NoError(t, db.Raw(`
SELECT COUNT(DISTINCT project_id)
FROM microsoft_allocations
WHERE status = 'allocated' AND mailbox = ?`, tt.mailbox).Scan(&projects).Error)
			}
			require.EqualValues(t, 2, projects)
		})
	}
}

func TestICloudProjectScopedActiveMigrationDownRejectsCrossProjectAliasMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	migrationsDir := allocMigrationsDir(t)
	require.NoError(t, goose.DownTo(sqlDB, migrationsDir, 120))
	seedAllocBase(t, db, "icloud", 1, 0, 0)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type)
VALUES (11, 'Other iCloud Migration Project', 'alloc', 'listed', 'public')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (21, 11, 'icloud', 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 1, 0, 0)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
VALUES (1000, 'icloud', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resources(id, primary_email, expire_at, for_sale, status, alias_count)
VALUES (1000, 'migration@icloud.com', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), TRUE, 'normal', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_aliases(resource_id, anonymous_id, email, status)
VALUES (1000, 'migration-alias', 'migration-alias@icloud.com', 'normal')`).Error)
	var aliasID uint
	require.NoError(t, db.Table("icloud_aliases").Select("id").
		Where("resource_id = 1000").Scan(&aliasID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type) VALUES
    ('migration-120-icloud-10', 'icloud'),
    ('migration-120-icloud-11', 'icloud')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_allocations(
    order_no, project_id, product_id, resource_id, alias_id,
    supply_scope, email
) VALUES
    ('migration-120-icloud-10', 10, 20, 1000, ?, 'public', 'migration-alias@icloud.com'),
    ('migration-120-icloud-11', 11, 21, 1000, ?, 'public', 'migration-alias@icloud.com')`,
		aliasID, aliasID).Error)

	err = goose.DownTo(sqlDB, migrationsDir, 119)
	require.ErrorContains(t, err, "chk_icloud_project_scoped_active_down_guard")
	version, versionErr := goose.GetDBVersion(sqlDB)
	require.NoError(t, versionErr)
	require.EqualValues(t, 120, version)
	var projects int64
	require.NoError(t, db.Raw(`
SELECT COUNT(DISTINCT project_id)
FROM icloud_allocations
WHERE status = 'allocated'`).Scan(&projects).Error)
	require.EqualValues(t, 2, projects)
}
