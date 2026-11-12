package icloud

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestICloudMaintenanceUpdatesDoNotBlockAnotherResourcesInsertMySQL(t *testing.T) {
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "00001_fixture.sql"), []byte(`-- +goose Up
CREATE TABLE icloud_resources(id BIGINT UNSIGNED PRIMARY KEY);
CREATE TABLE icloud_maintenance_runs (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 resource_id BIGINT UNSIGNED NOT NULL,
 validation_generation BIGINT UNSIGNED NOT NULL,
 kind VARCHAR(24) NOT NULL,
 status VARCHAR(24) NOT NULL,
 attempts INT NOT NULL,
 max_attempts INT NOT NULL,
 credential_revision BIGINT UNSIGNED NOT NULL,
 queued_at DATETIME(3) NOT NULL,
 started_at DATETIME(3) NULL,
 finished_at DATETIME(3) NULL,
 last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
 probe_recipient VARCHAR(320) NOT NULL DEFAULT '',
 probe_sender VARCHAR(320) NOT NULL DEFAULT '',
 probe_sent_at DATETIME(3) NULL,
 next_check_at DATETIME(3) NULL,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 UNIQUE INDEX uk_icloud_maintenance_resource_generation(resource_id, kind, validation_generation),
 INDEX idx_icloud_maintenance_resource_updated(resource_id, updated_at, id),
 INDEX idx_icloud_maintenance_status(status, updated_at, id),
 FOREIGN KEY(resource_id) REFERENCES icloud_resources(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose Down
DROP TABLE icloud_maintenance_runs;
DROP TABLE icloud_resources;
`), 0o600))
	server := testmysql.New("remail_icloud_maintenance_locks")
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })
	t.Run("bounded_recovery_preserves_live_and_finished_runs", func(t *testing.T) {
		db := server.Database(t, fixture)
		require.NoError(t, db.Exec("INSERT INTO icloud_resources(id) VALUES (1)").Error)
		now := time.Now().UTC().Truncate(time.Millisecond)
		old := now.Add(-time.Hour)
		runs := make([]iCloudMaintenanceRunModel, 103)
		for i := range runs {
			runs[i] = iCloudMaintenanceRunModel{
				ResourceID: 1, ValidationGeneration: uint64(i + 1), CredentialRevision: 1,
				Kind: iCloudMaintenanceAlias, Status: iCloudMaintenanceRunning,
				Attempts: 1, MaxAttempts: 1, QueuedAt: old, StartedAt: &old, CreatedAt: old, UpdatedAt: old,
			}
		}
		runs[101].StartedAt = &now
		runs[102].Status = iCloudMaintenanceSucceeded
		require.NoError(t, db.CreateInBatches(&runs, 100).Error)
		require.NoError(t, recoverStaleICloudProvisionRunsTx(context.Background(), db, 0, now))
		var failed int64
		require.NoError(t, db.Model(&iCloudMaintenanceRunModel{}).Where("status=?", iCloudMaintenanceFailed).Count(&failed).Error)
		require.EqualValues(t, 100, failed)
		require.NoError(t, recoverStaleICloudProvisionRunsTx(context.Background(), db, 0, now))
		require.NoError(t, db.Model(&iCloudMaintenanceRunModel{}).Where("status=?", iCloudMaintenanceFailed).Count(&failed).Error)
		require.EqualValues(t, 101, failed)
		var live, done iCloudMaintenanceRunModel
		require.NoError(t, db.First(&live, runs[101].ID).Error)
		require.Equal(t, iCloudMaintenanceRunning, live.Status)
		require.NoError(t, db.First(&done, runs[102].ID).Error)
		require.Equal(t, iCloudMaintenanceSucceeded, done.Status)
	})
	for _, test := range []struct {
		name   string
		seed   bool
		cancel bool
	}{
		{name: "empty_expired_range"},
		{name: "existing_expired_runs", seed: true},
		{name: "empty_cancel_range", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := server.Database(t, fixture)
			require.NoError(t, db.Exec("INSERT INTO icloud_resources(id) VALUES (1),(2)").Error)
			now := time.Now().UTC().Truncate(time.Millisecond)
			if test.seed {
				old := now.Add(-time.Hour)
				for _, id := range []uint{1, 2} {
					require.NoError(t, db.Create(&iCloudMaintenanceRunModel{
						ResourceID: id, ValidationGeneration: 1, CredentialRevision: 1,
						Kind: iCloudMaintenanceAlias, Status: iCloudMaintenanceRunning,
						Attempts: 1, MaxAttempts: 1, QueuedAt: old, StartedAt: &old, CreatedAt: old, UpdatedAt: old,
					}).Error)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ready := make(chan struct{}, 2)
			insert := make(chan struct{})
			results := make(chan error, 2)
			for _, id := range []uint{1, 2} {
				go func(resourceID uint) {
					results <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
						var locked uint
						if err := tx.Raw("SELECT id FROM icloud_resources WHERE id=? FOR UPDATE", resourceID).Scan(&locked).Error; err != nil {
							return err
						}
						var err error
						if test.cancel {
							err = cancelActiveICloudMaintenanceRunsTx(ctx, tx, resourceID, 0, now)
						} else {
							err = recoverStaleICloudProvisionRunsTx(ctx, tx, resourceID, now)
						}
						if err != nil {
							return err
						}
						ready <- struct{}{}
						select {
						case <-insert:
						case <-ctx.Done():
							return ctx.Err()
						}
						_, err = startICloudProvisionRunTx(ctx, tx, iCloudResourceModel{ID: resourceID, CredentialRevision: 1}, now)
						return err
					}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
				}(id)
			}
			for range 2 {
				select {
				case <-ready:
				case err := <-results:
					t.Fatalf("maintenance preparation failed: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			close(insert)
			for range 2 {
				select {
				case err := <-results:
					require.NoError(t, err, "different resources must be able to insert after maintenance cleanup")
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			var running int64
			require.NoError(t, db.Model(&iCloudMaintenanceRunModel{}).Where("status=?", iCloudMaintenanceRunning).Count(&running).Error)
			require.EqualValues(t, 2, running)
			if test.seed {
				var failed int64
				require.NoError(t, db.Model(&iCloudMaintenanceRunModel{}).Where("status=?", iCloudMaintenanceFailed).Count(&failed).Error)
				require.EqualValues(t, 2, failed)
			}
		})
	}
}
