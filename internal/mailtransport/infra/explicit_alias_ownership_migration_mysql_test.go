package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestExplicitAliasOwnershipMigrationBackfillsOldestSuperAdminMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	downgradeExplicitAliasOwnershipMigration(t, db)

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role) VALUES
    (3, 'oldest-super-admin@test.local', 'hash', 'super_admin'),
    (8, 'later-super-admin@test.local', 'hash', 'super_admin'),
    (20, 'legacy-supplier@test.local', 'hash', 'supplier')`).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (2000, 'microsoft', 20)",
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, email_address, email_domain, password, for_sale, status)
VALUES (2000, 'legacy-owner@outlook.com', 'outlook.com', 'secret', TRUE, 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, email, status)
VALUES (2000, 'legacy-explicit-alias@outlook.com', 'normal')`).Error)

	upgradeExplicitAliasOwnershipMigration(t, db)

	var ownerUserID uint
	require.NoError(t, db.Raw(
		"SELECT owner_user_id FROM explicit_aliases WHERE resource_id = 2000",
	).Scan(&ownerUserID).Error)
	require.Equal(t, uint(3), ownerUserID)
	require.Error(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, email, status)
VALUES (2000, 'missing-owner@outlook.com', 'normal')`).Error)
	require.Error(t, db.Exec("DELETE FROM users WHERE id = 3").Error)
}

func TestExplicitAliasOwnershipMigrationRejectsLegacyAliasesWithoutSuperAdminMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	downgradeExplicitAliasOwnershipMigration(t, db)

	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (20, 'ownerless-supplier@test.local', 'hash', 'supplier')",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id) VALUES (2001, 'microsoft', 20)",
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, email_address, email_domain, password, for_sale, status)
VALUES (2001, 'ownerless@outlook.com', 'outlook.com', 'secret', TRUE, 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, email, status)
VALUES (2001, 'ownerless-explicit-alias@outlook.com', 'normal')`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	err = goose.UpTo(sqlDB, mailTransportMigrationsDir(t), 8)
	require.Error(t, err)
}

func downgradeExplicitAliasOwnershipMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), 7))
}

func upgradeExplicitAliasOwnershipMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.UpTo(sqlDB, mailTransportMigrationsDir(t), 8))
}
