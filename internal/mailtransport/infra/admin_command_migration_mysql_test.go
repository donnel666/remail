package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdminMailTransportCommandReceiptMigrationConstraintsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9240, "normal")
	createMicrosoftAliasTestResource(t, db, 9241, "normal")

	assertMigrationPrimaryKey(t, db, "microsoft_token_refresh_requests", "operator_user_id,idempotency_key")
	assertMigrationPrimaryKey(t, db, "microsoft_alias_expedite_requests", "operator_user_id,idempotency_key")
	assertMigrationForeignKeyCount(t, db, "microsoft_token_refresh_requests", 3)
	assertMigrationForeignKeyCount(t, db, "microsoft_alias_expedite_requests", 2)
	assertMigrationColumnExists(t, db, "microsoft_token_refresh_requests", "reused")
	assertMigrationColumnExists(t, db, "microsoft_alias_expedite_requests", "reused")

	require.NoError(t, db.Exec(`
INSERT INTO microsoft_token_refresh_jobs(
    resource_id, operator_user_id, expected_credential_revision, status
) VALUES (9240, 9240, 1, 'queued')`).Error)
	var tokenJobID uint64
	require.NoError(t, db.Raw(`
SELECT id FROM microsoft_token_refresh_jobs WHERE resource_id = 9240
`).Scan(&tokenJobID).Error)
	require.NotZero(t, tokenJobID)

	require.NoError(t, db.Exec(`
INSERT INTO microsoft_token_refresh_requests(
    operator_user_id, idempotency_key, resource_id, job_id
) VALUES (?, 'same-command-key', 9240, ?)`, 9240, tokenJobID).Error)
	assert.Error(t, db.Exec(`
INSERT INTO microsoft_token_refresh_requests(
    operator_user_id, idempotency_key, resource_id, job_id
) VALUES (?, 'same-command-key', 9240, ?)`, 9240, tokenJobID).Error)
	// Idempotency scope is per operator, so a different administrator may use
	// the same opaque key while still pointing at a valid durable fact.
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_token_refresh_requests(
    operator_user_id, idempotency_key, resource_id, job_id
) VALUES (?, 'same-command-key', 9240, ?)`, 9241, tokenJobID).Error)
	assert.Error(t, db.Exec(`
INSERT INTO microsoft_token_refresh_requests(
    operator_user_id, idempotency_key, resource_id, job_id
) VALUES (9240, '', 9240, ?)`, tokenJobID).Error)
	assert.Error(t, db.Exec(`
INSERT INTO microsoft_token_refresh_requests(
    operator_user_id, idempotency_key, resource_id, job_id
) VALUES (9240, 'invalid-job', 9240, 999999999)`).Error)

	require.NoError(t, db.Exec(`
INSERT INTO microsoft_alias_expedite_requests(
    operator_user_id, idempotency_key, resource_id, reused
) VALUES (9240, 'same-alias-key', 9240, FALSE)`).Error)
	assert.Error(t, db.Exec(`
INSERT INTO microsoft_alias_expedite_requests(
    operator_user_id, idempotency_key, resource_id, reused
) VALUES (9240, 'same-alias-key', 9240, TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_alias_expedite_requests(
    operator_user_id, idempotency_key, resource_id, reused
) VALUES (9241, 'same-alias-key', 9240, TRUE)`).Error)
	assert.Error(t, db.Exec(`
INSERT INTO microsoft_alias_expedite_requests(
    operator_user_id, idempotency_key, resource_id, reused
) VALUES (9240, '', 9240, FALSE)`).Error)
	assert.Error(t, db.Exec(`
INSERT INTO microsoft_alias_expedite_requests(
    operator_user_id, idempotency_key, resource_id, reused
) VALUES (9240, 'invalid-resource', 999999999, FALSE)`).Error)
}

func TestAdminMailTransportCommandReceiptMigrationDownHasNoForeignKeyBlockerMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), 8))

	for _, table := range []string{
		"microsoft_alias_expedite_requests",
		"microsoft_token_refresh_requests",
		"microsoft_token_refresh_jobs",
	} {
		var count int64
		require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&count).Error)
		assert.Zero(t, count, table)
	}
	require.NoError(t, goose.UpTo(sqlDB, mailTransportMigrationsDir(t), 9))
}

func assertMigrationPrimaryKey(t *testing.T, db *gorm.DB, table string, expected string) {
	t.Helper()
	var columns string
	require.NoError(t, db.Raw(`
SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',')
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND index_name = 'PRIMARY'`, table).Scan(&columns).Error)
	assert.Equal(t, expected, columns)
}

func assertMigrationForeignKeyCount(t *testing.T, db *gorm.DB, table string, expected int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.referential_constraints
WHERE constraint_schema = DATABASE()
  AND table_name = ?`, table).Scan(&count).Error)
	assert.Equal(t, expected, count)
}

func assertMigrationColumnExists(t *testing.T, db *gorm.DB, table string, column string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND column_name = ?`, table, column).Scan(&count).Error)
	assert.EqualValues(t, 1, count)
}
