package infra

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestProtoSessionMigrationMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Join(filepath.Dir(file), "../../../migrations")
	baseline := testmysql.MigrationsThrough(t, source, 137)
	target := testmysql.MigrationsThrough(t, source, 138)
	server := testmysql.New("remail_proto_sessions")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })
	db := server.Database(t, baseline)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	for _, sql := range []string{
		`INSERT INTO users(id, email, password_hash, nickname, status, role)
         VALUES (1, 'session-migration@example.com', 'hash', 'migration', 'active', 'super_admin')`,
		`INSERT INTO email_resources(id, type, owner_user_id, version, updated_at) VALUES
         (100,'proto',1,4,'2020-01-01'), (101,'proto',1,8,'2020-01-01'),
         (102,'proto',1,2,'2020-01-01'), (103,'proto',1,3,'2020-01-01'),
         (200,'microsoft',1,11,'2020-01-01'), (201,'domain',1,11,'2020-01-01'),
         (202,'gmail',1,11,'2020-01-01'), (203,'icloud',1,11,'2020-01-01')`,
		`INSERT INTO proto_resources(id, owner_user_id, email_address, password, status, for_sale, version, validation_generation, last_safe_error) VALUES
         (100,1,'validation@proton.me','preserve-password','pending',TRUE,4,4,'proto_validation_todo'),
         (101,1,'history@proton.me','preserve-password','identifying',TRUE,8,8,'proto_history_todo'),
         (102,1,'disabled@proton.me','preserve-password','disabled',TRUE,2,2,'proto_validation_todo'),
         (103,1,'normal@proton.me','preserve-password','normal',TRUE,3,3,'')`,
		`INSERT INTO microsoft_resources(id,email_address,password,refresh_token,status)
         VALUES (200,'unchanged@outlook.com','keep-ms-password','keep-ms-token','normal')`,
		`INSERT INTO proto_maintenance_runs(resource_id,validation_generation,credential_revision,kind,status,last_safe_error)
         VALUES (100,4,1,'validation','uncertain','proto_validation_todo')`,
		`INSERT INTO projects(id,name,target_platform,status) VALUES
         (10,'Proto placeholder','test','listed'), (11,'Proto existing failure','test','listed')`,
		`INSERT INTO proto_project_history_scan_states(project_id,generation,status,after_id,through_id,scanned_count,matched_count,last_safe_error) VALUES
         (10,7,'uncertain',10,100,10,5,'proto_project_history_todo'),
         (11,9,'uncertain',20,100,20,2,'real history failure')`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	assertFacts := func() {
		var resources []Resource
		require.NoError(t, db.Order("id").Find(&resources).Error)
		require.Len(t, resources, 4)
		for i, expected := range []struct {
			status     string
			generation uint64
		}{{"pending", 5}, {"pending", 9}, {"disabled", 2}, {"normal", 3}} {
			require.Equal(t, expected.status, resources[i].Status)
			require.Equal(t, expected.generation, resources[i].ValidationGeneration)
			require.Equal(t, "preserve-password", resources[i].Password)
			require.True(t, resources[i].ForSale)
		}
		var roots []resourceRoot
		require.NoError(t, db.Where("id >= 200").Find(&roots).Error)
		require.Len(t, roots, 4)
		for _, root := range roots {
			require.EqualValues(t, 11, root.Version)
			require.Equal(t, 2020, root.UpdatedAt.Year())
		}
		var microsoft struct{ Password, RefreshToken, Status string }
		require.NoError(t, db.Table("microsoft_resources").Where("id = 200").Take(&microsoft).Error)
		require.Equal(t, "keep-ms-password", microsoft.Password)
		require.Equal(t, "keep-ms-token", microsoft.RefreshToken)
		require.Equal(t, "normal", microsoft.Status)
		var run MaintenanceRun
		require.NoError(t, db.Where("resource_id = 100").Take(&run).Error)
		require.Equal(t, "uncertain", run.Status)
		require.Equal(t, "proto_validation_todo", run.LastSafeError)
		var projects []ProjectHistoryState
		require.NoError(t, db.Order("project_id").Find(&projects).Error)
		require.Len(t, projects, 2)
		require.EqualValues(t, 8, projects[0].Generation)
		require.Equal(t, "pending", projects[0].Status)
		require.Zero(t, projects[0].AfterID)
		require.Zero(t, projects[0].ThroughID)
		require.Zero(t, projects[0].ScannedCount)
		require.Zero(t, projects[0].MatchedCount)
		require.Empty(t, projects[0].LastSafeError)
		require.EqualValues(t, 9, projects[1].Generation)
		require.Equal(t, "real history failure", projects[1].LastSafeError)
	}
	require.NoError(t, platform.RunMigrations(sqlDB, target))
	require.True(t, db.Migrator().HasTable(&sessionRecord{}))
	assertFacts()
	// A committed CREATE TABLE/requeue followed by a missing Goose marker must
	// retain session ciphertext and never advance placeholder generations twice.
	payload := []byte("test-ciphertext-persistence")
	require.NoError(t, db.Create(&sessionRecord{ResourceID: 100, CredentialRevision: 1, Version: 1, Payload: payload}).Error)
	require.NoError(t, db.Exec("DELETE FROM goose_db_version WHERE version_id = 138").Error)
	require.NoError(t, platform.RunMigrations(sqlDB, target))
	assertFacts()
	var stored sessionRecord
	require.NoError(t, db.Where("resource_id = 100").Take(&stored).Error)
	require.Equal(t, payload, stored.Payload)
	require.Error(t, goose.DownTo(sqlDB, target, 137), "rollback must not discard durable session keys")
	require.True(t, db.Migrator().HasTable(&sessionRecord{}))
	version, err := goose.GetDBVersion(sqlDB)
	require.NoError(t, err)
	require.EqualValues(t, 138, version)
	require.NoError(t, db.Where("resource_id = 100").Delete(&sessionRecord{}).Error)
	require.NoError(t, goose.DownTo(sqlDB, target, 137))
	require.False(t, db.Migrator().HasTable(&sessionRecord{}))
	assertFacts()
}
