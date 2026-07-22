package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestExplicitAliasFixedOwnerMigrationRepairsAndGuardsRowsMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), 38))

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role) VALUES
    (1, 'fixed-owner@test.local', 'hash', 'super_admin'),
    (2, 'wrong-owner@test.local', 'hash', 'user')`).Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id, type, owner_user_id) VALUES (2039, 'microsoft', 2)").Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, resource_type, email_address, email_domain, password, for_sale, status)
VALUES (2039, 'microsoft', 'fixed-owner@outlook.com', 'outlook.com', 'secret', TRUE, 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (2039, 2, 'existing@outlook.com', 'normal')`).Error)

	require.NoError(t, goose.UpTo(sqlDB, mailTransportMigrationsDir(t), 39))

	var ownerUserID uint
	require.NoError(t, db.Table("explicit_aliases").Select("owner_user_id").
		Where("resource_id = ?", 2039).Scan(&ownerUserID).Error)
	require.Equal(t, uint(1), ownerUserID)
	require.Error(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (2039, 2, 'rejected@outlook.com', 'normal')`).Error)
	require.Error(t, db.Table("explicit_aliases").Where("resource_id = ?", 2039).
		Update("owner_user_id", 2).Error)
	require.NoError(t, db.Table("explicit_aliases").Select("owner_user_id").
		Where("resource_id = ?", 2039).Scan(&ownerUserID).Error)
	require.Equal(t, uint(1), ownerUserID)

	var checkCount int
	require.NoError(t, sqlDB.QueryRow(`
SELECT COUNT(*)
FROM information_schema.table_constraints
WHERE constraint_schema = DATABASE()
  AND table_name = 'explicit_aliases'
  AND constraint_name = 'chk_explicit_aliases_owner_user_id'
  AND constraint_type = 'CHECK'`).Scan(&checkCount))
	require.Equal(t, 1, checkCount)
}

func TestExplicitAliasFixedOwnerMigrationRequiresUserOneSuperAdminMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailTransportMigrationsDir(t), 38))

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role)
VALUES (3, 'legacy-owner@test.local', 'hash', 'super_admin')`).Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id, type, owner_user_id) VALUES (2040, 'microsoft', 3)").Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, resource_type, email_address, email_domain, password, for_sale, status)
VALUES (2040, 'microsoft', 'legacy-owner@outlook.com', 'outlook.com', 'secret', TRUE, 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (2040, 3, 'legacy@outlook.com', 'normal')`).Error)

	err = goose.UpTo(sqlDB, mailTransportMigrationsDir(t), 39)
	require.ErrorContains(t, err, "migration 00039 requires users.id=1")

	var ownerUserID uint
	require.NoError(t, db.Table("explicit_aliases").Select("owner_user_id").
		Where("resource_id = ?", 2040).Scan(&ownerUserID).Error)
	require.Equal(t, uint(3), ownerUserID)
}
