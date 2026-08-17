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

func TestAdminTaskViewRepoListsICloudRefreshTasks(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:governance-icloud-refresh?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE icloud_account_onboarding_tasks (
    id INTEGER PRIMARY KEY,
    resource_id INTEGER,
    task_kind TEXT NOT NULL,
    status TEXT NOT NULL,
    dispatch_status TEXT NOT NULL,
    attempts INTEGER NOT NULL,
    max_attempts INTEGER NOT NULL,
    expected_credential_revision INTEGER NOT NULL,
    created_at DATETIME NOT NULL,
    started_at DATETIME,
    finished_at DATETIME,
    updated_at DATETIME NOT NULL
);
INSERT INTO icloud_account_onboarding_tasks(
    id, resource_id, task_kind, status, dispatch_status, attempts, max_attempts,
    expected_credential_revision, created_at, started_at, finished_at, updated_at
) VALUES
    (201, 7, 'refresh', 'processing', 'pending', 0, 5, 9, '2026-08-17 08:00:00', NULL, NULL, '2026-08-17 08:00:00'),
    (202, 7, 'refresh', 'processing', 'running', 1, 5, 9, '2026-08-17 08:01:00', '2026-08-17 08:01:01', NULL, '2026-08-17 08:01:01'),
    (203, 7, 'refresh', 'waiting', 'waiting', 2, 5, 9, '2026-08-17 08:02:00', '2026-08-17 08:02:01', NULL, '2026-08-17 08:02:02'),
    (204, 7, 'refresh', 'completed', 'succeeded', 2, 5, 9, '2026-08-17 08:03:00', '2026-08-17 08:03:01', '2026-08-17 08:03:02', '2026-08-17 08:03:02'),
    (205, 7, 'refresh', 'failed', 'failed', 5, 5, 9, '2026-08-17 08:04:00', '2026-08-17 08:04:01', '2026-08-17 08:04:02', '2026-08-17 08:04:02'),
    (206, 8, 'refresh', 'processing', 'pending', 0, 5, 4, '2026-08-17 08:05:00', NULL, NULL, '2026-08-17 08:05:00'),
    (207, 7, 'onboarding', 'processing', 'pending', 0, 5, 0, '2026-08-17 08:06:00', NULL, NULL, '2026-08-17 08:06:00');
`).Error)

	repo := NewAdminTaskViewRepo(db)
	items, total, succeeded, err := repo.listForResource(context.Background(), governanceapp.AdminTaskListFilter{
		BizID: 7, Source: governanceapp.AdminTaskSourceICloudRefresh, Limit: 20,
	}, iCloudRefreshTaskSelect)
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
	require.Equal(t, int64(1), succeeded)
	require.Len(t, items, 5)
	require.Contains(t, iCloudResourceTaskUnion, iCloudRefreshTaskSelect)

	statuses := make(map[string]string, len(items))
	for _, item := range items {
		statuses[item.TaskID()] = item.Status
		require.Equal(t, governanceapp.AdminTaskBizICloudResource, item.BizType)
		require.Equal(t, uint64(7), item.BizID)
		require.Equal(t, governanceapp.AdminTaskKindRefresh, item.Kind)
		require.Equal(t, uint64(9), *item.CredentialRevision)
	}
	require.Equal(t, map[string]string{
		"icloud_refresh:201": governanceapp.AdminTaskStatusQueued,
		"icloud_refresh:202": governanceapp.AdminTaskStatusRunning,
		"icloud_refresh:203": governanceapp.AdminTaskStatusUncertain,
		"icloud_refresh:204": governanceapp.AdminTaskStatusSucceeded,
		"icloud_refresh:205": governanceapp.AdminTaskStatusFailed,
	}, statuses)

	task, err := repo.FindByRef(context.Background(), governanceapp.AdminTaskRef{
		Source: governanceapp.AdminTaskSourceICloudRefresh,
		ID:     203,
	})
	require.NoError(t, err)
	require.Equal(t, "icloud_refresh:203", task.TaskID())
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, task.Status)
}

func TestAdminTaskViewRepoListsActiveICloudOnboardingImports(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:governance-icloud-onboarding?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE icloud_resource_imports (
    id INTEGER PRIMARY KEY, dispatch_status TEXT, attempts INTEGER, max_attempts INTEGER,
    accepted_count INTEGER, skipped_count INTEGER, imported_count INTEGER,
    created_at DATETIME, started_at DATETIME, finished_at DATETIME, updated_at DATETIME
);
CREATE TABLE icloud_resource_import_items (import_id INTEGER, outcome TEXT, category TEXT);
CREATE TABLE icloud_account_onboarding_imports (
    id INTEGER PRIMARY KEY, status TEXT, accepted_count INTEGER, completed_count INTEGER,
    failed_count INTEGER, created_at DATETIME, updated_at DATETIME
);
CREATE TABLE icloud_account_onboarding_tasks (
    id INTEGER PRIMARY KEY, import_id INTEGER, attempts INTEGER, max_attempts INTEGER, started_at DATETIME
);
INSERT INTO icloud_resource_imports(
    id, dispatch_status, attempts, max_attempts, accepted_count, skipped_count,
    imported_count, created_at, started_at, finished_at, updated_at
) VALUES (99, 'running', 1, 3, 1, 0, 0, '2026-08-16 08:06:00', '2026-08-16 08:06:01', NULL, '2026-08-16 08:06:02');
INSERT INTO icloud_account_onboarding_imports(
    id, status, accepted_count, completed_count, failed_count, created_at, updated_at
) VALUES (23, 'processing', 3, 1, 1, '2026-08-16 08:00:00', '2026-08-16 08:05:00');
INSERT INTO icloud_account_onboarding_tasks(id, import_id, attempts, max_attempts, started_at) VALUES
    (31, 23, 1, 5, '2026-08-16 08:00:01'),
    (32, 23, 2, 5, '2026-08-16 08:00:02');
`).Error)

	items, total, succeeded, err := NewAdminTaskViewRepo(db).ListForICloudImports(
		context.Background(),
		governanceapp.AdminTaskListFilter{
			BizType: governanceapp.AdminTaskBizICloudResourceImport,
			Source:  governanceapp.AdminTaskSourceICloudOnboarding,
			Status:  governanceapp.AdminTaskStatusRunning,
			Limit:   1,
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Zero(t, succeeded)
	require.Len(t, items, 1)
	require.Equal(t, "icloud_onboarding:23", items[0].TaskID())
	require.Equal(t, governanceapp.AdminTaskStatusRunning, items[0].Status)
	require.Equal(t, 2, items[0].Attempts)
	require.Equal(t, &governanceapp.AdminTaskProgress{Total: 3, Processed: 2, Succeeded: 1, Failed: 1, ReasonCounts: []governanceapp.AdminTaskReasonCount{}}, items[0].Progress)
}
