package infra

import (
	"database/sql"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
INSERT INTO users(id, email, password_hash, enabled, role)
VALUES
    (9301, 'source-owner@test.local', 'hash', 1, 'supplier'),
    (9302, 'target-owner@test.local', 'hash', 1, 'admin')`).Error)
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
	require.Equal(t, "legacy", legacyDispatchStatus)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_binding_mailboxes(
    resource_id, resource_type, owner_user_id, account_email,
    binding_address, status
) VALUES (
    9401, 'microsoft', 9301, 'managed@outlook.com',
    'binding@test.local', 'pending'
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO resource_validation_jobs(
    resource_id, resource_type, owner_user_id, expected_credential_revision,
    status, max_attempts
) VALUES (9401, 'microsoft', 9301, 1, 'succeeded', 3)`).Error)
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

	var validationOwner uint
	var inboundOwner uint
	require.NoError(t, db.Raw("SELECT owner_user_id FROM resource_validation_jobs WHERE resource_id = 9401").Scan(&validationOwner).Error)
	require.NoError(t, db.Raw("SELECT owner_user_id FROM inbound_mails WHERE resource_id = 9401").Scan(&inboundOwner).Error)
	require.Equal(t, uint(9301), validationOwner)
	require.Equal(t, uint(9301), inboundOwner)

	require.Error(t, db.Exec("UPDATE microsoft_resources SET quality_score = 101 WHERE id = 9401").Error)
	require.Error(t, db.Exec(`
INSERT INTO resource_validation_jobs(resource_id, resource_type, owner_user_id, status)
VALUES (999999, 'microsoft', 9301, 'failed')`).Error)
	require.Error(t, db.Exec(`
INSERT INTO resource_validation_jobs(resource_id, resource_type, owner_user_id, status)
VALUES (9401, 'microsoft', 999999, 'failed')`).Error)

	require.NoError(t, db.Exec(`
INSERT INTO microsoft_token_refresh_jobs(
    resource_id, operator_user_id, expected_credential_revision, status
) VALUES (9401, 9302, 1, 'queued')`).Error)
	require.Error(t, db.Exec(`
INSERT INTO microsoft_token_refresh_jobs(
    resource_id, operator_user_id, expected_credential_revision, status
) VALUES (9401, 9302, 1, 'running')`).Error)
	require.NoError(t, db.Exec(`
UPDATE microsoft_token_refresh_jobs
SET status = 'succeeded', finished_at = CURRENT_TIMESTAMP(3)
WHERE resource_id = 9401`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_token_refresh_jobs(
    resource_id, operator_user_id, expected_credential_revision, status
) VALUES (9401, 9302, 1, 'queued')`).Error)

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_jobs(
    resource_id, operator_user_id, expected_credential_revision,
    recipient, status, max_attempts
) VALUES (
    9401, 9302, 1, 'managed@outlook.com', 'queued', 3
)`).Error)

	require.NoError(t, db.Exec(`
INSERT INTO admin_resource_bulk_commands(
    operator_user_id, action, selection_mode, selection_json,
    selection_fingerprint, idempotency_key, max_resource_id, status
) VALUES (
    9302, 'publish', 'filter', JSON_OBJECT('type', 'microsoft'),
    REPEAT('a', 64), 'same-key', 9401, 'queued'
)`).Error)
	require.Error(t, db.Exec(`
INSERT INTO admin_resource_bulk_commands(
    operator_user_id, action, selection_mode, selection_json,
    selection_fingerprint, idempotency_key, max_resource_id, status
) VALUES (
    9302, 'publish', 'filter', JSON_OBJECT('type', 'microsoft'),
    REPEAT('a', 64), 'same-key', 9401, 'queued'
)`).Error)

	var permissionCount int64
	require.NoError(t, db.Table("casbin_rule").
		Where("v0 IN ? AND ((v1 = ? AND v2 IN ?) OR (v1 = ? AND v2 = ?))",
			[]string{"role:admin", "role:super_admin"},
			"mailtransport:binding", []string{"read", "write"},
			"governance:task", "read").
		Count(&permissionCount).Error)
	require.Equal(t, int64(6), permissionCount)

	// Application rollback must not rewrite or delete historical owner
	// snapshots. Migration 00009 therefore keeps the split resource/user FKs
	// instead of attempting to restore the incompatible legacy composite FK.
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, coreMigrationsDir(t), 8))
	require.NoError(t, db.Raw("SELECT owner_user_id FROM resource_validation_jobs WHERE resource_id = 9401").Scan(&validationOwner).Error)
	require.NoError(t, db.Raw("SELECT owner_user_id FROM inbound_mails WHERE resource_id = 9401").Scan(&inboundOwner).Error)
	require.Equal(t, uint(9301), validationOwner)
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
