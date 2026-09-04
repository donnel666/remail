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

func TestAdminTaskViewRepoListsGmailWorkflowViews(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:governance-gmail-tasks?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE email_resources (
    id INTEGER PRIMARY KEY,
    type TEXT NOT NULL
);
CREATE TABLE gmail_resources (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL,
    validation_failures INTEGER NOT NULL,
    credential_revision INTEGER NOT NULL,
    validation_generation INTEGER NOT NULL,
    last_checked_at DATETIME NULL,
    updated_at DATETIME NOT NULL
);
CREATE TABLE gmail_maintenance_runs (
    id INTEGER PRIMARY KEY,
    resource_id INTEGER NOT NULL,
    validation_generation INTEGER NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL,
    max_attempts INTEGER NOT NULL,
    credential_revision INTEGER NOT NULL,
    queued_at DATETIME NOT NULL,
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    last_safe_error TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE TABLE mailmatch_admin_resource_fetch_states (
    email_resource_id INTEGER PRIMARY KEY,
    status TEXT NOT NULL,
    generation INTEGER NOT NULL,
    failures INTEGER NOT NULL,
    operation_kind TEXT NOT NULL,
    expected_credential_revision INTEGER NOT NULL,
    requested_at DATETIME NULL,
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    updated_at DATETIME NOT NULL,
    fetched_count INTEGER NOT NULL,
    stored_count INTEGER NOT NULL,
    matched_count INTEGER NOT NULL
)`).Error)

	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type) VALUES (7001, 'gmail');
INSERT INTO gmail_resources(
    id, status, validation_failures, credential_revision,
    validation_generation, last_checked_at, updated_at
) VALUES (7001, 'pending', 0, 2, 2, NULL, ?)`, now).Error)
	older := now.Add(-time.Hour)
	require.NoError(t, db.Exec(`
INSERT INTO gmail_maintenance_runs(
    id, resource_id, validation_generation, kind, status, attempts, max_attempts,
    credential_revision, queued_at, started_at, finished_at, last_safe_error,
    created_at, updated_at
) VALUES (9100, 7001, 1, 'validation', 'succeeded', 1, 3, 1, ?, ?, ?, '', ?, ?)`,
		older, older, older, older, older).Error)
	require.NoError(t, db.Exec(`
INSERT INTO gmail_maintenance_runs(
    id, resource_id, validation_generation, kind, status, attempts, max_attempts,
    credential_revision, queued_at, started_at, finished_at, last_safe_error,
    created_at, updated_at
) VALUES (9101, 7001, 2, 'validation', 'queued', 0, 3, 2, ?, NULL, NULL, '', ?, ?)`,
		now, now, now).Error)
	require.NoError(t, db.Exec(`
INSERT INTO gmail_maintenance_runs(
    id, resource_id, validation_generation, kind, status, attempts, max_attempts,
    credential_revision, queued_at, started_at, finished_at, last_safe_error,
    created_at, updated_at
) VALUES (9102, 7001, 2, 'history', 'canceled', 1, 3, 2, ?, ?, ?, 'Disabled by administrator.', ?, ?)`,
		now, now, now, now, now).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_admin_resource_fetch_states(
    email_resource_id, status, generation, failures, operation_kind,
    expected_credential_revision, requested_at, started_at, finished_at,
    updated_at, fetched_count, stored_count, matched_count
) VALUES (7001, 'normal', 1, 0, 'gmail_resource_fetch', 2, ?, ?, ?, ?, 1, 1, 1)`,
		now, now, now, now).Error)

	repo := NewAdminTaskViewRepo(db)
	exists, err := repo.GmailResourceExists(context.Background(), 7001)
	require.NoError(t, err)
	require.True(t, exists)

	items, total, succeeded, err := repo.ListForGmailResource(context.Background(), governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizGmailResource,
		BizID:   7001,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(4), total)
	require.Equal(t, int64(2), succeeded)
	require.Len(t, items, 4)
	byID := make(map[string]governanceapp.AdminTaskView, len(items))
	for _, item := range items {
		byID[item.TaskID()] = item
	}
	require.Equal(t, governanceapp.AdminTaskStatusSucceeded, byID["gmail_validation:9100"].Status)
	require.Equal(t, governanceapp.AdminTaskStatusQueued, byID["gmail_validation:9101"].Status)
	require.Equal(t, governanceapp.AdminTaskStatusCanceled, byID["gmail_history:9102"].Status)
	require.Equal(t, governanceapp.AdminTaskStatusSucceeded, byID["fetch:7001"].Status)
}
