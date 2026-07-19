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
	require.Equal(t, int64(5), total)
	require.Equal(t, int64(3), succeeded)
	require.Len(t, items, 5)

	byID := make(map[string]governanceapp.AdminTaskView, len(items))
	for i := range items {
		byID[items[i].TaskID()] = items[i]
	}
	require.Contains(t, byID, "import:6201")
	require.Contains(t, byID, "alias:6301")
	require.Contains(t, byID, "alias_schedule:6001")
	require.Contains(t, byID, "token:6001")
	require.Contains(t, byID, "fetch:6001")
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, byID["alias:6301"].Status)
	require.Equal(t, governanceapp.AdminTaskStatusUncertain, byID["alias_schedule:6001"].Status)
	require.Equal(t, governanceapp.AdminTaskStatusSucceeded, byID["fetch:6001"].Status)
	require.Equal(t, governanceapp.AdminTaskKindHistory, byID["fetch:6001"].Kind)
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

func TestAdminTaskViewRepoFindsDurableTaskFactsMySQL(t *testing.T) {
	db := newGovernanceMySQLTestDB(t)
	seedGovernanceTaskFacts(t, db)
	repo := NewAdminTaskViewRepo(db)
	for _, testCase := range []struct {
		ref     governanceapp.AdminTaskRef
		kind    string
		bizType string
	}{
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceImport, ID: 6201}, kind: governanceapp.AdminTaskKindImport, bizType: governanceapp.AdminTaskBizMicrosoftResourceImport},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceAlias, ID: 6301}, kind: governanceapp.AdminTaskKindAlias, bizType: governanceapp.AdminTaskBizMicrosoftResource},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceAliasSchedule, ID: 6001}, kind: governanceapp.AdminTaskKindAlias, bizType: governanceapp.AdminTaskBizMicrosoftResource},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceToken, ID: 6001}, kind: governanceapp.AdminTaskKindToken, bizType: governanceapp.AdminTaskBizMicrosoftResource},
		{ref: governanceapp.AdminTaskRef{Source: governanceapp.AdminTaskSourceFetch, ID: 6001}, kind: governanceapp.AdminTaskKindHistory, bizType: governanceapp.AdminTaskBizMicrosoftResource},
	} {
		task, err := repo.FindByRef(context.Background(), testCase.ref)
		require.NoError(t, err, testCase.ref.String())
		require.Equal(t, testCase.ref.String(), task.TaskID())
		require.Equal(t, testCase.kind, task.Kind)
		require.Equal(t, testCase.bizType, task.BizType)
	}

	_, err := repo.FindByRef(context.Background(), governanceapp.AdminTaskRef{Source: "validation", ID: 6101})
	require.ErrorIs(t, err, governanceapp.ErrInvalidAdminTaskQuery)
}

func seedGovernanceTaskFacts(t *testing.T, db *gorm.DB) {
	t.Helper()
	base := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, status, role)
VALUES (5001, 'governance-admin@test.local', 'hash', 'active', 'admin')`).Error)
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
INSERT INTO resource_imports(
    id, owner_user_id, operator_user_id, resource_type, source_object_key,
	    status, imported_count, accepted_count, skipped_count, dispatch_status,
	    attempts, max_attempts, claim_token, created_at, updated_at
) VALUES (
    6201, 5001, 5001, 'microsoft', 'private/never-return-import.txt',
    'imported', 1, 1, 2, 'succeeded',
	    1, 3, 'never-return-claim', ?, ?
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
UPDATE microsoft_resources
SET token_refresh_status = 'normal',
    token_refresh_generation = 1,
    token_refresh_failures = 1,
    token_refresh_expected_credential_revision = 4,
    token_refresh_operator_user_id = 5001,
    token_refresh_idempotency_key = 'governance-token-view',
    token_refresh_requested_at = ?,
    token_refresh_finished_at = ?,
    updated_at = ?
WHERE id = 6001`, base.Add(5*time.Minute), base.Add(5*time.Minute), base.Add(5*time.Minute)).Error)

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_states(
    email_resource_id, status, generation, failures, operation_kind,
    operator_user_id, expected_credential_revision,
    fetched_count, stored_count, matched_count,
    request_id, requested_at, finished_at, created_at, updated_at
) VALUES (
    6001, 'normal', 2, 1, 'resource_history',
    5001, 4, 0, 0, 0,
    'governance-fetch-view', ?, ?, ?, ?
)`, base.Add(6*time.Minute), base.Add(6*time.Minute), base.Add(6*time.Minute), base.Add(6*time.Minute)).Error)

}
