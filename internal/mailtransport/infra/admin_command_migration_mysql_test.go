package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftMaintenanceMigrationRoundTripMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), 29))
	require.True(t, db.Migrator().HasTable("microsoft_token_refresh_jobs"))
	require.True(t, db.Migrator().HasTable("microsoft_token_refresh_requests"))
	require.False(t, db.Migrator().HasColumn(&MicrosoftTokenRefreshStateModel{}, "token_refresh_generation"))
	require.False(t, db.Migrator().HasColumn(&MicrosoftAliasScheduleModel{}, "generation"))
	createMicrosoftAliasTestResource(t, db, 993001, "normal")
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_token_refresh_jobs(
    resource_id, operator_user_id, expected_credential_revision,
    status, attempts, max_attempts, request_id, path
) VALUES (993001, 993001, 1, 'running', 2, 3, 'legacy-active', '/legacy')`).Error)

	require.NoError(t, goose.UpTo(sqlDB, mailTransportMigrationsDir(t), 30))
	require.False(t, db.Migrator().HasTable("microsoft_token_refresh_jobs"))
	require.False(t, db.Migrator().HasTable("microsoft_token_refresh_requests"))
	require.True(t, db.Migrator().HasColumn(&MicrosoftTokenRefreshStateModel{}, "token_refresh_generation"))
	require.True(t, db.Migrator().HasColumn(&MicrosoftAliasScheduleModel{}, "generation"))
	var state struct {
		Status   string `gorm:"column:token_refresh_status"`
		Failures int    `gorm:"column:token_refresh_failures"`
	}
	require.NoError(t, db.Table("microsoft_resources").Where("id = 993001").Take(&state).Error)
	require.Equal(t, "pending", state.Status)
	require.Zero(t, state.Failures, "legacy execution attempts are not business failures")
}

func TestAdminMailTransportCommandReceiptMigrationConstraintsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	createMicrosoftAliasTestResource(t, db, 9240, "normal")
	createMicrosoftAliasTestResource(t, db, 9241, "normal")

	assertMigrationPrimaryKey(t, db, "microsoft_alias_expedite_requests", "operator_user_id,idempotency_key")
	assertMigrationForeignKeyCount(t, db, "microsoft_alias_expedite_requests", 2)
	assertMigrationColumnExists(t, db, "microsoft_resources", "token_refresh_generation")
	assertMigrationColumnExists(t, db, "microsoft_resources", "token_refresh_idempotency_scope")
	assertMigrationColumnExists(t, db, "microsoft_alias_expedite_requests", "reused")

	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET token_refresh_status = 'pending', token_refresh_generation = 1,
    token_refresh_operator_user_id = 9240,
    token_refresh_idempotency_key = 'same-command-key'
WHERE id = 9240`).Error)
	assert.Error(t, db.Exec(`
UPDATE microsoft_resources
SET token_refresh_status = 'pending', token_refresh_generation = 1,
    token_refresh_operator_user_id = 9240,
    token_refresh_idempotency_key = 'same-command-key'
WHERE id = 9241`).Error)
	// The same opaque key remains independent across administrators.
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET token_refresh_status = 'pending', token_refresh_generation = 1,
    token_refresh_operator_user_id = 9241,
    token_refresh_idempotency_key = 'same-command-key'
WHERE id = 9241`).Error)

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
