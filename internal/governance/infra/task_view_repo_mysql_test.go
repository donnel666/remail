package infra

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var governanceMySQLTestServer = testmysql.New("remail_governance_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = governanceMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newGovernanceMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return governanceMySQLTestServer.Database(t, governanceMigrationsDir(t))
}

func governanceMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestAdminTaskViewRepoAggregatesSafeResourceTasksMySQL(t *testing.T) {
	db := newGovernanceMySQLTestDB(t)
	seedGovernanceTaskFacts(t, db)
	repo := NewAdminTaskViewRepo(db)

	items, total, succeeded, err := repo.ListForMicrosoftResource(context.Background(), governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizMicrosoftResource,
		BizID:   6001,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(6), total)
	require.Equal(t, int64(2), succeeded)
	require.Len(t, items, 6)

	byID := make(map[string]governanceapp.AdminTaskView, len(items))
	for i := range items {
		byID[items[i].TaskID()] = items[i]
	}
	require.Contains(t, byID, "validation:6101")
	require.Contains(t, byID, "import:6201")
	require.Contains(t, byID, "alias:6301")
	require.Contains(t, byID, "alias_schedule:6001")
	require.Contains(t, byID, "token:6401")
	require.Contains(t, byID, "fetch:6501")
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, byID["alias:6301"].Status)
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, byID["alias_schedule:6001"].Status)
	require.Equal(t, governanceapp.AdminTaskStatusCanceled, byID["fetch:6501"].Status)
	require.NotNil(t, byID["import:6201"].Progress)
	require.Equal(t, int64(3), byID["import:6201"].Progress.Total)
	require.Equal(t, []governanceapp.AdminTaskReasonCount{
		{Reason: "format", Count: 1},
		{Reason: "other", Count: 1},
	}, byID["import:6201"].Progress.ReasonCounts)

	aliasItems, aliasTotal, _, err := repo.ListForMicrosoftResource(context.Background(), governanceapp.AdminTaskListFilter{
		BizType: governanceapp.AdminTaskBizMicrosoftResource,
		BizID:   6001,
		Kind:    governanceapp.AdminTaskKindAlias,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), aliasTotal)
	require.Len(t, aliasItems, 2)

	scheduleTask, err := repo.FindByRef(context.Background(), governanceapp.AdminTaskRef{
		Source: governanceapp.AdminTaskSourceAliasSchedule,
		ID:     6001,
	})
	require.NoError(t, err)
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, scheduleTask.Status)
	require.Nil(t, scheduleTask.FinishedAt)
}

func TestAdminTaskViewRepoFindsBulkWithoutLeakingSelectionMySQL(t *testing.T) {
	db := newGovernanceMySQLTestDB(t)
	seedGovernanceTaskFacts(t, db)
	repo := NewAdminTaskViewRepo(db)
	for _, testCase := range []struct {
		ref     governanceapp.AdminTaskRef
		kind    string
		bizType string
	}{
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceValidation, ID: 6101}, kind: governanceapp.AdminTaskKindValidation, bizType: governanceapp.AdminTaskBizMicrosoftResource},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceImport, ID: 6201}, kind: governanceapp.AdminTaskKindImport, bizType: governanceapp.AdminTaskBizMicrosoftResourceImport},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceAlias, ID: 6301}, kind: governanceapp.AdminTaskKindAlias, bizType: governanceapp.AdminTaskBizMicrosoftResource},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceAliasSchedule, ID: 6001}, kind: governanceapp.AdminTaskKindAlias, bizType: governanceapp.AdminTaskBizMicrosoftResource},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceToken, ID: 6401}, kind: governanceapp.AdminTaskKindToken, bizType: governanceapp.AdminTaskBizMicrosoftResource},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceFetch, ID: 6501}, kind: governanceapp.AdminTaskKindFetch, bizType: governanceapp.AdminTaskBizMicrosoftResource},
	} {
		task, err := repo.FindByRef(context.Background(), testCase.ref)
		require.NoError(t, err, testCase.ref.String())
		require.Equal(t, testCase.ref.String(), task.TaskID())
		require.Equal(t, testCase.kind, task.Kind)
		require.Equal(t, testCase.bizType, task.BizType)
	}

	task, err := repo.FindByRef(context.Background(), governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceBulk, ID: 6601})
	require.NoError(t, err)
	require.Equal(t, "bulk:6601", task.TaskID())
	require.Equal(t, governanceapp.AdminTaskBizMicrosoftResourceBulk, task.BizType)
	require.Equal(t, governanceapp.AdminTaskKindBulkPublish, task.Kind)
	require.NotNil(t, task.Progress)
	require.Equal(t, int64(10), task.Progress.Total)
	require.Equal(t, int64(8), task.Progress.Processed)
	require.Equal(t, int64(5), task.Progress.Succeeded)
	require.Equal(t, int64(2), task.Progress.Skipped)
	require.Equal(t, int64(1), task.Progress.Failed)
	require.Equal(t, []governanceapp.AdminTaskReasonCount{
		{Reason: "active_allocation", Count: 2},
		{Reason: "other", Count: 1},
	}, task.Progress.ReasonCounts)

	_, err = repo.FindByRef(context.Background(), governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceValidation, ID: 999999})
	require.ErrorIs(t, err, governanceapp.ErrAdminTaskNotFound)
}

func seedGovernanceTaskFacts(t *testing.T, db *gorm.DB) {
	t.Helper()
	base := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, enabled, role)
VALUES (5001, 'governance-admin@test.local', 'hash', 1, 'admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id, version, created_at, updated_at)
VALUES (6001, 'microsoft', 5001, 1, ?, ?)`, base, base).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(
    id, resource_type, email_address, email_domain, password,
    client_id, refresh_token, credential_revision, status, quality_score,
    created_at, updated_at
) VALUES (
    6001, 'microsoft', 'task-view@outlook.com', 'outlook.com', 'never-return-password',
    'never-return-client', 'never-return-token', 4, 'normal', 80, ?, ?
)`, base, base).Error)

	require.NoError(t, db.Exec(`
INSERT INTO resource_validation_jobs(
    id, resource_id, resource_type, owner_user_id, expected_credential_revision,
    status, attempts, max_attempts, last_safe_error, created_at, updated_at
) VALUES (
    6101, 6001, 'microsoft', 5001, 4,
    'failed', 3, 3, 'safe validation category', ?, ?
)`, base.Add(time.Minute), base.Add(time.Minute)).Error)

	require.NoError(t, db.Exec(`
INSERT INTO resource_imports(
    id, owner_user_id, operator_user_id, resource_type, source_object_key,
    status, imported_count, accepted_count, skipped_count, dispatch_status,
    attempts, max_attempts, claim_token, dispatch_token, created_at, updated_at
) VALUES (
    6201, 5001, 5001, 'microsoft', 'private/never-return-import.txt',
    'imported', 1, 1, 2, 'succeeded',
    1, 3, 'never-return-claim', 'never-return-dispatch', ?, ?
)`, base.Add(2*time.Minute), base.Add(2*time.Minute)).Error)
	require.NoError(t, db.Exec(`
INSERT INTO resource_import_items(import_id, resource_id, line_number, outcome, category)
VALUES
    (6201, 6001, 1, 'imported', ''),
    (6201, 6001, 2, 'skipped', 'format'),
    (6201, 6001, 3, 'skipped', 'password=never-return')`).Error)

	require.NoError(t, db.Exec(`
INSERT INTO microsoft_alias_attempts(
    id, resource_id, candidate, status, quota_at, category, last_safe_error,
    was_attempted, uncertain_since, created_at, updated_at
) VALUES (
    6301, 6001, 'never-return-alias@outlook.com', 'uncertain', ?, 'request',
    'safe alias category', 1, ?, ?, ?
)`, base.Add(3*time.Minute), base.Add(3*time.Minute), base.Add(3*time.Minute), base.Add(3*time.Minute)).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_alias_schedules(
    resource_id, status, next_run_at, attempts, claim_token, last_safe_error,
    last_run_at, created_at, updated_at
) VALUES (
    6001, 'pending', ?, 1, '', 'safe schedule category',
    ?, ?, ?
)`, time.Now().UTC().Add(24*time.Hour), base.Add(4*time.Minute), base, base.Add(8*time.Minute)).Error)

	require.NoError(t, db.Exec(`
INSERT INTO microsoft_token_refresh_jobs(
    id, resource_id, operator_user_id, expected_credential_revision, status,
    attempts, max_attempts, claim_token, dispatch_token, created_at, updated_at
) VALUES (
    6401, 6001, 5001, 4, 'succeeded',
    1, 3, 'never-return-token-claim', 'never-return-token-dispatch', ?, ?
)`, base.Add(5*time.Minute), base.Add(5*time.Minute)).Error)

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_jobs(
    id, resource_id, operator_user_id, expected_credential_revision, recipient,
    status, attempts, max_attempts, fetched_count, stored_count, matched_count,
    claim_token, dispatch_token, created_at, updated_at
) VALUES (
    6501, 6001, 5001, 4, 'task-view@outlook.com',
    'canceled', 1, 3, 5, 4, 2,
    'never-return-fetch-claim', 'never-return-fetch-dispatch', ?, ?
)`, base.Add(6*time.Minute), base.Add(6*time.Minute)).Error)

	require.NoError(t, db.Exec(`
INSERT INTO admin_resource_bulk_commands(
    id, operator_user_id, action, selection_mode, selection_json,
    selection_fingerprint, max_resource_id, checkpoint_resource_id, status,
    matched_count, processed_count, affected_count, skipped_count, reason_buckets,
    attempts, max_attempts, claim_token, dispatch_token, created_at, updated_at
) VALUES (
    6601, 5001, 'publish', 'filter', JSON_OBJECT('secret', 'never-return-selection'),
    REPEAT('a', 64), 6001, 6001, 'running',
    10, 8, 5, 2, JSON_OBJECT('active_allocation', 2, 'password=never-return', 1),
    1, 3, 'never-return-bulk-claim', 'never-return-bulk-dispatch', ?, ?
)`, base.Add(7*time.Minute), base.Add(7*time.Minute)).Error)
}
