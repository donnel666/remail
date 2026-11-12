package icloud

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOwnedICloudConcurrentFilterBatchReplayMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	db := iCloudOnboardingMigrationMySQL.Database(t, testmysql.MigrationsThrough(t, source, 110))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, platform.RunMigrations(sqlDB, testmysql.MigrationsThrough(t, source, 146)))
	require.NoError(t, db.Exec("INSERT INTO users (id, email, password_hash, role, status) VALUES (7, 'owner@example.test', 'unused-test-hash', 'supplier', 'active')").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources (id, type, owner_user_id) VALUES (1, 'icloud', 7)").Error)
	require.NoError(t, db.Exec("INSERT INTO icloud_resources (id, primary_email, status, expire_at) VALUES (1, 'first@example.test', 'normal', ?)", time.Now().Add(time.Hour)).Error)
	s := NewService(db, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filter := AdminICloudResourceListFilter{Suffix: "example.test"}
	type outcome struct {
		result *AdminICloudBulkResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, 4)
	for range 4 {
		go func() {
			<-start
			result, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, filter, "concurrent-request", "request", "/test")
			results <- outcome{result, err}
		}()
	}
	close(start)
	var first *AdminICloudBulkResult
	for range 4 {
		select {
		case got := <-results:
			require.NoError(t, got.err)
			require.Equal(t, []uint{1}, got.result.AffectedResourceIDs)
			if first == nil {
				first = got.result
			} else {
				require.Equal(t, first, got.result)
			}
		case <-ctx.Done():
			t.Fatal("concurrent batch requests did not finish")
		}
	}
	require.NoError(t, db.Exec("INSERT INTO email_resources (id, type, owner_user_id) VALUES (2, 'icloud', 7)").Error)
	require.NoError(t, db.Exec("INSERT INTO icloud_resources (id, primary_email, status, expire_at) VALUES (2, 'new@example.test', 'normal', ?)", time.Now().Add(time.Hour)).Error)
	replay, err := s.ApplyOwnedICloudBatchByFilter(ctx, 7, AdminICloudDelete, filter, "concurrent-request", "later-retry", "/test")
	require.NoError(t, err)
	require.Equal(t, first, replay)
	var status string
	require.NoError(t, db.Table("icloud_resources").Select("status").Where("id = 2").Scan(&status).Error)
	require.Equal(t, iCloudResourceNormal, status)
}

func TestICloudBatchCancelsRunsCommittedAfterSelectionMySQL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "migrations"))
	for _, owned := range []bool{false, true} {
		name := "admin"
		if owned {
			name = "owned"
		}
		t.Run(name, func(t *testing.T) {
			db := iCloudOnboardingMigrationMySQL.Database(t, testmysql.MigrationsThrough(t, source, 110))
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, platform.RunMigrations(sqlDB, testmysql.MigrationsThrough(t, source, 146)))
			require.NoError(t, db.Exec("INSERT INTO users (id, email, password_hash, role, status) VALUES (7, 'owner@example.test', 'unused-test-hash', 'supplier', 'active')").Error)
			require.NoError(t, db.Exec("INSERT INTO email_resources (id, type, owner_user_id) VALUES (1, 'icloud', 7)").Error)
			now := time.Now().UTC().Truncate(time.Millisecond)
			require.NoError(t, db.Exec("INSERT INTO icloud_resources (id, primary_email, status, expire_at) VALUES (1, 'first@example.test', 'normal', ?)", now.Add(time.Hour)).Error)
			run := iCloudMaintenanceRunModel{
				ResourceID: 1, ValidationGeneration: 1, CredentialRevision: 1,
				Kind: iCloudMaintenanceAlias, Status: iCloudMaintenanceRunning,
				Attempts: 1, MaxAttempts: 1, QueuedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
			}
			inserted := false
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:concurrent_maintenance_run", func(tx *gorm.DB) {
				if tx.Statement.Table != "email_resources" || inserted {
					return
				}
				// The filter has established a snapshot, but this resource is not locked yet.
				inserted = true
				_ = tx.AddError(db.Create(&run).Error)
			}))
			t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove("test:concurrent_maintenance_run")) })
			service := NewService(db, nil, nil)
			filter := AdminICloudResourceListFilter{Suffix: "example.test"}
			var result *AdminICloudBulkResult
			if owned {
				result, err = service.ApplyOwnedICloudBatchByFilter(context.Background(), 7, AdminICloudDelete, filter, "cancel-run", "request", "/test")
			} else {
				result, err = service.ApplyAdminICloudBatch(context.Background(), AdminICloudDisable,
					AdminICloudResourceSelection{Mode: "filter", Filter: &filter}, nil, 7, "cancel-run", "request", "/test")
			}
			require.NoError(t, err)
			require.True(t, inserted)
			require.Equal(t, []uint{1}, result.AffectedResourceIDs)
			require.NoError(t, db.First(&run, run.ID).Error)
			require.Equal(t, iCloudMaintenanceCanceled, run.Status, "a successful batch must cancel even a newly committed run")
		})
	}
}
