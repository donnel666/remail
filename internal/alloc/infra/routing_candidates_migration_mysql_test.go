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
	require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id = 86").Error)

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
