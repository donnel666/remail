package infra

import (
	"context"
	"strings"
	"testing"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newProtoTaskViewDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := "file:governance-proto-task-view-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE email_resources (
    id INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL
);
CREATE TABLE proto_resources (
    id INTEGER PRIMARY KEY,
    resource_type TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL
);
CREATE TABLE proto_resource_imports (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL,
    dispatch_status TEXT NOT NULL,
    dispatch_attempts INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    accepted_count INTEGER NOT NULL,
    imported_count INTEGER NOT NULL,
    skipped_count INTEGER NOT NULL,
    failed_count INTEGER NOT NULL,
    created_at DATETIME NOT NULL,
    started_at DATETIME,
    finished_at DATETIME,
    updated_at DATETIME NOT NULL
);
CREATE TABLE proto_resource_import_items (
    import_id INTEGER NOT NULL,
    resource_id INTEGER,
    outcome TEXT NOT NULL,
    category TEXT NOT NULL
);
CREATE TABLE proto_maintenance_runs (
    id INTEGER PRIMARY KEY,
    resource_id INTEGER NOT NULL,
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
CREATE TABLE mailmatch_admin_resource_fetch_states (
    email_resource_id INTEGER PRIMARY KEY,
    operation_kind TEXT NOT NULL,
    status TEXT NOT NULL,
    failures INTEGER NOT NULL DEFAULT 0,
    expected_credential_revision INTEGER NOT NULL DEFAULT 1,
    requested_at DATETIME,
    started_at DATETIME,
    finished_at DATETIME,
    updated_at DATETIME NOT NULL,
    fetched_count INTEGER NOT NULL DEFAULT 0,
    stored_count INTEGER NOT NULL DEFAULT 0
);
INSERT INTO email_resources(id, type, owner_user_id) VALUES (42, 'proto', 7);
INSERT INTO proto_resources(id, resource_type, owner_user_id) VALUES (42, 'proto', 7);
INSERT INTO proto_resource_imports(
    id, status, dispatch_status, dispatch_attempts, accepted_count, imported_count,
    skipped_count, failed_count, created_at, started_at, finished_at, updated_at
) VALUES (9001, 'imported', 'done', 1, 3, 1, 1, 0,
          '2026-09-07 01:00:00', '2026-09-07 01:00:01', '2026-09-07 01:00:02', '2026-09-07 01:00:02');
INSERT INTO proto_resource_import_items(import_id, resource_id, outcome, category) VALUES
    (9001, 42, 'imported', ''),
    (9001, 42, 'skipped', 'format'),
    (9001, NULL, 'skipped', 'duplicate');
INSERT INTO proto_maintenance_runs(
    id, resource_id, kind, status, attempts, max_attempts, credential_revision,
    queued_at, started_at, finished_at, updated_at
) VALUES
    (9101, 42, 'validation', 'failed', 1, 3, 2,
     '2026-09-07 01:01:00', '2026-09-07 01:01:01', '2026-09-07 01:01:02', '2026-09-07 01:01:02'),
    (9102, 42, 'history', 'queued', 0, 3, 2,
     '2026-09-07 01:02:00', NULL, NULL, '2026-09-07 01:02:00');
`).Error)
	return db
}

func TestAdminTaskViewRepoListsProtoImportAndMaintenanceTasks(t *testing.T) {
	db := newProtoTaskViewDB(t)
	repo := NewAdminTaskViewRepo(db)

	items, total, succeeded, err := repo.ListForProtoResource(context.Background(), governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizProtoResource,
		BizID:   42,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Equal(t, int64(1), succeeded)
	require.Len(t, items, 3)
	require.Equal(t, "proto_history:9102", items[0].TaskID())
	require.Equal(t, governanceapp.AdminTaskStatusQueued, items[0].Status)
	require.Equal(t, "proto_validation:9101", items[1].TaskID())
	require.Equal(t, governanceapp.AdminTaskStatusFailed, items[1].Status)
	require.Equal(t, "proto_import:9001", items[2].TaskID())
	require.Equal(t, governanceapp.AdminTaskStatusSucceeded, items[2].Status)

	imports, total, succeeded, err := repo.ListForProtoImports(context.Background(), governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizProtoResourceImport,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, int64(1), succeeded)
	require.Len(t, imports, 1)
	require.Equal(t, "proto_import:9001", imports[0].TaskID())
	require.Equal(t, int64(3), imports[0].Progress.Total)
	require.Equal(t, int64(2), imports[0].Progress.Processed)

	task, err := repo.FindByRef(context.Background(), governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceProtoImport, ID: 9001})
	require.NoError(t, err)
	require.NotNil(t, task.Progress)
	require.Equal(t, []governanceapp.AdminTaskReasonCount{{Reason: "duplicate", Count: 1}, {Reason: "format", Count: 1}}, task.Progress.ReasonCounts)
}

func TestAdminTaskQueryServiceUsesOptionalProtoRepository(t *testing.T) {
	db := newProtoTaskViewDB(t)
	repo := NewAdminTaskViewRepo(db)
	result, err := governanceapp.NewAdminTaskQueryService(repo).List(context.Background(), governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizProtoResource,
		BizID:   42,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Len(t, result.Items, 3)
}

func TestAdminTaskViewRepoReportsMissingProtoSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:governance-proto-task-view-empty?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	repo := NewAdminTaskViewRepo(db)
	items, total, succeeded, err := repo.ListForProtoResource(context.Background(), governanceapp.AdminTaskListFilter{BizID: 42, Limit: 20})
	require.Error(t, err)
	require.Empty(t, items)
	require.Zero(t, total)
	require.Zero(t, succeeded)
	imports, total, succeeded, err := repo.ListForProtoImports(context.Background(), governanceapp.AdminTaskListFilter{Limit: 20})
	require.Error(t, err)
	require.Empty(t, imports)
	require.Zero(t, total)
	require.Zero(t, succeeded)
}

func TestAdminTaskViewRepoFindsProtoFetchThroughSharedResourceID(t *testing.T) {
	db := newProtoTaskViewDB(t)
	require.NoError(t, db.Exec(`INSERT INTO mailmatch_admin_resource_fetch_states
    (email_resource_id, operation_kind, status, updated_at)
    VALUES (42, 'proto_resource_fetch', 'abnormal', '2026-09-07 02:00:00')`).Error)
	repo := NewAdminTaskViewRepo(db)
	task, err := repo.FindByRef(context.Background(), governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceFetch, ID: 42})
	require.NoError(t, err)
	require.Equal(t, governanceapp.AdminTaskBizProtoResource, task.BizType)
	require.Equal(t, governanceapp.AdminTaskStatusFailed, task.Status)
}

func TestProtoImportFailureProgressIncludesEveryFailedLine(t *testing.T) {
	db := newProtoTaskViewDB(t)
	require.NoError(t, db.Exec(`UPDATE proto_resource_imports SET status = 'failed',
    dispatch_status = 'failed', attempts = 3, max_attempts = 3,
    accepted_count = 3, imported_count = 0, skipped_count = 0, failed_count = 3
    WHERE id = 9001`).Error)
	task, err := NewAdminTaskViewRepo(db).FindByRef(context.Background(), governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceProtoImport, ID: 9001})
	require.NoError(t, err)
	require.EqualValues(t, 3, task.Progress.Total)
	require.EqualValues(t, 3, task.Progress.Processed)
	require.EqualValues(t, 3, task.Progress.Failed)
	require.Equal(t, 3, task.Attempts)
	require.Equal(t, 3, task.MaxAttempts)
}
