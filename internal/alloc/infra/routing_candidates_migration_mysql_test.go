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
