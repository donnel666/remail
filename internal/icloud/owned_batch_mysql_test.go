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
