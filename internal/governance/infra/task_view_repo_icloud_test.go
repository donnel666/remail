package infra

import (
	"context"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdminTaskViewRepoAttachesICloudImportReasons(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:governance-icloud-task-reasons?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE icloud_resource_import_items (
    import_id INTEGER NOT NULL,
    outcome TEXT NOT NULL,
    category TEXT NOT NULL
);
INSERT INTO icloud_resource_import_items(import_id, outcome, category) VALUES
    (7101, 'skipped', 'format'),
    (7101, 'skipped', 'password=never-return')`).Error)

	items := []governanceapp.AdminTaskView{{
		Ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceICloudImport, ID: 7101},
		Progress: &governanceapp.AdminTaskProgress{
			ReasonCounts: []governanceapp.AdminTaskReasonCount{},
		},
	}}
	repo := NewAdminTaskViewRepo(db)
	require.NoError(t, repo.attachImportReasonCounts(
		context.Background(),
		items,
		governanceapp.AdminTaskSourceICloudImport,
		"icloud_resource_import_items",
	))
	require.Equal(t, []governanceapp.AdminTaskReasonCount{
		{Reason: "format", Count: 1},
		{Reason: "other", Count: 1},
	}, items[0].Progress.ReasonCounts)
}

func TestAdminTaskViewRepoListsDistinctICloudMaintenanceRuns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:governance-icloud-maintenance?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE icloud_maintenance_runs (
    id INTEGER PRIMARY KEY,
    resource_id INTEGER NOT NULL,
    validation_generation INTEGER NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL,
    max_attempts INTEGER NOT NULL,
    credential_revision INTEGER NOT NULL,
    queued_at DATETIME NOT NULL,
    started_at DATETIME,
    finished_at DATETIME,
    updated_at DATETIME NOT NULL
);
INSERT INTO icloud_maintenance_runs(
    id, resource_id, validation_generation, kind, status, attempts,
    max_attempts, credential_revision, queued_at, started_at, finished_at, updated_at
) VALUES
    (101, 7, 4, 'validation', 'succeeded', 1, 3, 2, '2026-08-08 00:00:00', '2026-08-08 00:00:01', '2026-08-08 00:00:02', '2026-08-08 00:00:02'),
    (102, 7, 5, 'alias', 'running', 1, 3, 2, '2026-08-08 00:01:00', '2026-08-08 00:01:01', NULL, '2026-08-08 00:01:01');
`).Error)

	repo := NewAdminTaskViewRepo(db)
	items, total, succeeded, err := repo.listForResource(context.Background(), governanceapp.AdminTaskListFilter{
		BizID: 7, Limit: 20,
	}, iCloudValidationTaskSelect)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Equal(t, int64(1), succeeded)
	require.Len(t, items, 2)
	require.Equal(t, "icloud_validation:102", items[0].TaskID())
	require.Equal(t, governanceapp.AdminTaskKindAlias, items[0].Kind)
	require.Equal(t, "icloud_validation:101", items[1].TaskID())
	require.WithinDuration(t, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), items[1].QueuedAt.UTC(), time.Second)
}
