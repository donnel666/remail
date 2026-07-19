package infra

import (
	"database/sql"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCoreAsyncStateSchemaMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.False(t, db.Migrator().HasTable("admin_resource_bulk_commands"))
	require.True(t, db.Migrator().HasColumn(&ResourceImportModel{}, "generation"))
	require.False(t, db.Migrator().HasColumn("resource_imports", "dispatch_token"))
	require.False(t, db.Migrator().HasColumn("resource_imports", "dispatched_at"))
}

func TestCoreAsyncStateMigrationResumesAfterGenerationWasCommittedMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, coreMigrationsDir(t), 30))

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, enabled, role)
VALUES (990311, 'core-async-migration@test.local', 'hash', 1, 'admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO resource_imports(
    owner_user_id, operator_user_id, resource_type, source_object_key,
    status, dispatch_status, claim_token, started_at
) VALUES
(
    990311, 990311, 'microsoft', 'imports/partial.txt',
    'processing', 'queued', '', NULL
),
(
    990311, 990311, 'microsoft', 'imports/running.txt',
    'processing', 'running', REPEAT('r', 36), CURRENT_TIMESTAMP(3)
),
(
    990311, 990311, 'microsoft', 'imports/imported.txt',
    'imported', 'legacy', '', NULL
),
(
    990311, 990311, 'microsoft', 'imports/failed.txt',
    'failed', 'legacy', '', NULL
)`).Error)

	// Reproduce the production failure: MySQL committed these statements, but
	// Goose never recorded migration 31 because the following UPDATE failed.
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS admin_resource_bulk_commands").Error)
	require.NoError(t, db.Exec(`
ALTER TABLE resource_imports
ADD COLUMN generation BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER max_attempts`).Error)

	require.NoError(t, goose.UpTo(sqlDB, coreMigrationsDir(t), 31))
	var rows []struct {
		SourceObjectKey string     `gorm:"column:source_object_key"`
		DispatchStatus  string     `gorm:"column:dispatch_status"`
		Generation      uint64     `gorm:"column:generation"`
		ClaimToken      string     `gorm:"column:claim_token"`
		StartedAt       *time.Time `gorm:"column:started_at"`
	}
	require.NoError(t, db.Table("resource_imports").Where("owner_user_id = 990311").Order("source_object_key").Find(&rows).Error)
	require.Len(t, rows, 4)
	require.Equal(t, "failed", rows[0].DispatchStatus)
	require.Equal(t, "succeeded", rows[1].DispatchStatus)
	require.Equal(t, "pending", rows[2].DispatchStatus)
	require.Equal(t, "pending", rows[3].DispatchStatus)
	for _, row := range rows {
		require.Equal(t, uint64(1), row.Generation)
		if row.DispatchStatus == "pending" {
			require.Empty(t, row.ClaimToken)
			require.Nil(t, row.StartedAt)
		}
	}
	require.False(t, db.Migrator().HasColumn("resource_imports", "dispatch_token"))
	require.False(t, db.Migrator().HasColumn("resource_imports", "dispatched_at"))
	require.False(t, db.Migrator().HasTable("admin_resource_bulk_commands"))
	require.Error(t, db.Table("resource_imports").Where("source_object_key = 'imports/partial.txt'").Update("dispatch_status", "legacy").Error)
	var indexColumns string
	require.NoError(t, db.Raw(`
SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',')
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND table_name = 'resource_imports'
  AND index_name = 'idx_resource_imports_pending_generation'`).Scan(&indexColumns).Error)
	require.Equal(t, "status,dispatch_status,generation,id", indexColumns)
}

func TestResourceValidationGenerationMigrationTakesOverInflightAssignmentsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, coreMigrationsDir(t), 33))

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, enabled, role)
VALUES (9801, 'validation-migration-owner@test.local', 'hash', 1, 'admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, server_address, status)
VALUES (9802, 9801, '127.0.0.1', 'online')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
VALUES (9803, 'microsoft', 9801), (9804, 'domain', 9801)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, resource_type, email_address, password, status)
VALUES (9803, 'microsoft', 'validation-migration@outlook.com', 'secret', 'validating')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_resources(id, resource_type, owner_user_id, domain, mail_server_id, purpose, status)
VALUES (9804, 'domain', 9801, 'validation-migration.example.com', 9802, 'not_sale', 'validating')`).Error)

	require.NoError(t, goose.UpTo(sqlDB, coreMigrationsDir(t), 34))
	for _, table := range []string{"microsoft_resources", "domain_resources"} {
		var status string
		var generation uint64
		var failures int
		require.NoError(t, db.Table(table).Select("status, validation_generation, validation_failures").Where("id IN ?", []uint{9803, 9804}).Row().Scan(&status, &generation, &failures))
		require.Equal(t, "pending", status)
		require.Equal(t, uint64(2), generation)
		require.Zero(t, failures)
		require.Error(t, db.Table(table).Where("id IN ?", []uint{9803, 9804}).Update("validation_failures", 4).Error)
	}
}

func TestAdminMicrosoftMigrationBackfillsConcurrencyFactsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, coreMigrationsDir(t), 8))

	legacyUpdatedAt := time.Date(2026, time.July, 1, 2, 3, 4, 0, time.UTC)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, enabled, role)
VALUES (9101, 'legacy-owner@test.local', 'hash', 1, 'supplier')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id, created_at, updated_at)
VALUES (9201, 'microsoft', 9101, ?, ?)`, legacyUpdatedAt, legacyUpdatedAt).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(
    id, resource_type, email_address, email_domain, password,
    client_id, refresh_token, status, quality_score, created_at, updated_at
) VALUES (9201, 'microsoft', 'legacy@outlook.com', 'outlook.com', 'password',
          'client-id', 'refresh-token', 'normal', 80, ?, ?)`, legacyUpdatedAt, legacyUpdatedAt).Error)

	require.NoError(t, goose.UpTo(sqlDB, coreMigrationsDir(t), 9))

	var rootVersion uint64
	require.NoError(t, db.Raw("SELECT version FROM email_resources WHERE id = 9201").Scan(&rootVersion).Error)
	require.Equal(t, uint64(1), rootVersion)

	var credentialRevision uint64
	var credentialUpdatedAt time.Time
	require.NoError(t, db.Raw(`
SELECT credential_revision, credential_updated_at
FROM microsoft_resources
WHERE id = 9201`).Row().Scan(&credentialRevision, &credentialUpdatedAt))
	require.Equal(t, uint64(1), credentialRevision)
	require.WithinDuration(t, legacyUpdatedAt, credentialUpdatedAt, time.Millisecond)
}

func TestAdminMicrosoftMigrationConstraintsAndOwnerHistoryMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
	INSERT INTO users(id, email, password_hash, status, role)
	VALUES
	    (9301, 'source-owner@test.local', 'hash', 'active', 'supplier'),
	    (9302, 'target-owner@test.local', 'hash', 'active', 'admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
VALUES (9401, 'microsoft', 9301)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(
    id, resource_type, email_address, email_domain, password,
    client_id, refresh_token, status, quality_score
) VALUES (
    9401, 'microsoft', 'managed@outlook.com', 'outlook.com', 'password',
    'client-id', 'refresh-token', 'normal', 75
)`).Error)

	// The pre-feature insert shape remains valid during an application rollback.
	require.NoError(t, db.Exec(`
INSERT INTO resource_imports(
    owner_user_id, resource_type, source_object_key, status
) VALUES (
    9301, 'microsoft', 'core/imports/legacy.txt', 'processing'
)`).Error)
	var legacyOperator sql.NullInt64
	var legacyDispatchStatus string
	require.NoError(t, db.Raw(`
SELECT operator_user_id, dispatch_status
FROM resource_imports
WHERE source_object_key = 'core/imports/legacy.txt'`).Row().Scan(&legacyOperator, &legacyDispatchStatus))
	require.False(t, legacyOperator.Valid)
	require.Equal(t, "pending", legacyDispatchStatus)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_binding_mailboxes(
    resource_id, resource_type, owner_user_id, account_email,
    binding_address, status
) VALUES (
    9401, 'microsoft', 9301, 'managed@outlook.com',
    'binding@test.local', 'pending'
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO inbound_mails(
    envelope_from, recipient, resource_id, resource_type, owner_user_id,
    source_object_key, status
) VALUES (
    'sender@test.local', 'binding@test.local', 9401, 'microsoft', 9301,
    'mailtransport/inbound/history.eml', 'stored'
)`).Error)

	require.NoError(t, db.Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Exec("UPDATE email_resources SET owner_user_id = ?, version = version + 1 WHERE id = ?", 9302, 9401).Error; err != nil {
			return err
		}
		return txDB.Exec("UPDATE microsoft_binding_mailboxes SET owner_user_id = ? WHERE resource_id = ?", 9302, 9401).Error
	}))

	var currentOwner uint
	require.NoError(t, db.Raw("SELECT owner_user_id FROM email_resources WHERE id = 9401").Scan(&currentOwner).Error)
	require.Equal(t, uint(9302), currentOwner)
	require.NoError(t, db.Raw("SELECT owner_user_id FROM microsoft_binding_mailboxes WHERE resource_id = 9401").Scan(&currentOwner).Error)
	require.Equal(t, uint(9302), currentOwner)

	var inboundOwner uint
	require.NoError(t, db.Raw("SELECT owner_user_id FROM inbound_mails WHERE resource_id = 9401").Scan(&inboundOwner).Error)
	require.Equal(t, uint(9301), inboundOwner)

	require.Error(t, db.Exec("UPDATE microsoft_resources SET quality_score = 101 WHERE id = 9401").Error)

	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET token_refresh_status = 'pending',
    token_refresh_generation = 1,
    token_refresh_expected_credential_revision = 1,
    token_refresh_operator_user_id = 9302,
    token_refresh_idempotency_key = 'migration-state-9401'
WHERE id = 9401`).Error)
	require.Error(t, db.Exec(`
UPDATE microsoft_resources
SET token_refresh_failures = 4
WHERE id = 9401`).Error)
	require.Error(t, db.Exec(`
UPDATE microsoft_resources
SET token_refresh_status = 'invalid'
WHERE id = 9401`).Error)

	var permissionCount int64
	require.NoError(t, db.Table("casbin_rule").
		Where("v0 IN ? AND ((v1 = ? AND v2 IN ?) OR (v1 = ? AND v2 = ?))",
			[]string{"role:admin", "role:super_admin"},
			"mailtransport:binding", []string{"read", "write"},
			"governance:task", "read").
		Count(&permissionCount).Error)
	require.Equal(t, int64(6), permissionCount)

	// Application rollback must not rewrite or delete historical owner
	// snapshots. Migration 00009 therefore keeps the historical owner on mail
	// facts instead of attempting to rewrite it with the current resource owner.
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, coreMigrationsDir(t), 8))
	require.NoError(t, db.Raw("SELECT owner_user_id FROM inbound_mails WHERE resource_id = 9401").Scan(&inboundOwner).Error)
	require.Equal(t, uint(9301), inboundOwner)

	// The relaxed rollback schema is also a supported source for a later
	// forward deployment of the same migration.
	require.NoError(t, goose.UpTo(sqlDB, coreMigrationsDir(t), 9))
}

func TestAdminResourceCommandReceiptForwardMigrationAndConstraintsMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, coreMigrationsDir(t), 9))
	require.False(t, db.Migrator().HasTable(&AdminResourceCommandReceiptModel{}))
	require.NoError(t, goose.UpTo(sqlDB, coreMigrationsDir(t), 10))
	require.True(t, db.Migrator().HasTable(&AdminResourceCommandReceiptModel{}))

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, enabled, role)
VALUES
    (9501, 'receipt-operator-one@test.local', 'hash', 1, 'admin'),
    (9502, 'receipt-operator-two@test.local', 'hash', 1, 'admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO admin_resource_command_receipts(
    operator_user_id, idempotency_key, operation, subject,
    request_fingerprint, reservation_token, status, result_json
) VALUES (
    9501, 'same-key', 'core.admin_resource.disable', 'microsoft_resource:1',
    REPEAT('a', 64), '00000000-0000-7000-8000-000000000001', 'succeeded', JSON_OBJECT()
)`).Error)
	require.Error(t, db.Exec(`
INSERT INTO admin_resource_command_receipts(
    operator_user_id, idempotency_key, operation, subject,
    request_fingerprint, reservation_token, status, result_json
) VALUES (
    9501, 'same-key', 'core.admin_resource.publish', 'microsoft_resource:2',
    REPEAT('b', 64), '00000000-0000-7000-8000-000000000002', 'succeeded', JSON_OBJECT()
)`).Error, "one operator and key share a command namespace across operations and resources")
	require.NoError(t, db.Exec(`
INSERT INTO admin_resource_command_receipts(
    operator_user_id, idempotency_key, operation, subject,
    request_fingerprint, reservation_token, status, result_json
) VALUES (
    9502, 'same-key', 'core.admin_resource.publish', 'microsoft_resource:2',
    REPEAT('b', 64), '00000000-0000-7000-8000-000000000003', 'succeeded', JSON_OBJECT()
)`).Error, "different operators have independent idempotency namespaces")
	require.Error(t, db.Exec(`
INSERT INTO admin_resource_command_receipts(
    operator_user_id, idempotency_key, operation, subject,
    request_fingerprint, reservation_token, status, result_json
) VALUES (
    9501, 'invalid-result', 'core.admin_resource.disable', 'microsoft_resource:3',
    REPEAT('c', 64), '00000000-0000-7000-8000-000000000004', 'succeeded', NULL
)`).Error, "a succeeded receipt must contain a stable business result")
}
