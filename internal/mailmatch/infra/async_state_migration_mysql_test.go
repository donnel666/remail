package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestProjectHistoryStateMigrationRoundTripRehydratesLegacyPlannerMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailmatchMigrationsDir(t), 32))

	require.NoError(t, db.Exec(`
INSERT INTO projects(
    id, name, target_platform, history_scan_status, history_scan_generation,
    history_scan_failures, history_scan_scanned_count, history_scan_matched_count,
    history_scan_skipped_count, history_scan_request_id, history_scan_requested_at
) VALUES
    (990321, 'history-pending', 'outlook', 'pending', 8, 1, 11, 7, 2, 'request-pending', CURRENT_TIMESTAMP(3)),
    (990322, 'history-processing', 'outlook', 'processing', 9, 3, 13, 8, 3, 'request-processing', CURRENT_TIMESTAMP(3)),
    (990323, 'history-normal', 'outlook', 'normal', 10, 0, 17, 9, 4, 'request-normal', CURRENT_TIMESTAMP(3)),
    (990324, 'history-abnormal', 'outlook', 'abnormal', 11, 3, 19, 10, 5, 'request-abnormal', CURRENT_TIMESTAMP(3))`).Error)

	require.NoError(t, goose.DownTo(sqlDB, mailmatchMigrationsDir(t), 31))
	var planners []struct {
		ProjectID    uint   `gorm:"column:project_id"`
		Status       string `gorm:"column:status"`
		Attempts     int    `gorm:"column:attempts"`
		ScannedCount int    `gorm:"column:scanned_count"`
		RequestID    string `gorm:"column:request_id"`
	}
	require.NoError(t, db.Table("mailmatch_project_history_scan_jobs").Where("project_id BETWEEN ? AND ?", 990321, 990324).Order("project_id").Find(&planners).Error)
	require.Equal(t, []struct {
		ProjectID    uint   `gorm:"column:project_id"`
		Status       string `gorm:"column:status"`
		Attempts     int    `gorm:"column:attempts"`
		ScannedCount int    `gorm:"column:scanned_count"`
		RequestID    string `gorm:"column:request_id"`
	}{
		{ProjectID: 990321, Status: "queued", Attempts: 1, ScannedCount: 11, RequestID: "request-pending"},
		{ProjectID: 990322, Status: "queued", Attempts: 2, ScannedCount: 13, RequestID: "request-processing"},
	}, planners)

	require.NoError(t, goose.UpTo(sqlDB, mailmatchMigrationsDir(t), 32))
	var states []struct {
		Status     string `gorm:"column:history_scan_status"`
		Generation uint64 `gorm:"column:history_scan_generation"`
	}
	require.NoError(t, db.Table("projects").Where("id IN ?", []uint{990321, 990322}).Order("id").Find(&states).Error)
	require.Equal(t, []struct {
		Status     string `gorm:"column:history_scan_status"`
		Generation uint64 `gorm:"column:history_scan_generation"`
	}{
		{Status: "pending", Generation: 1},
		{Status: "pending", Generation: 1},
	}, states)
}

func TestMailmatchFetchStateMigrationNormalizesLegacyCountsMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailmatchMigrationsDir(t), 32))
	seedMailmatchOrder(t, db, "OR_MIGRATION_COUNTS")

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_fetch_jobs(
    order_no, purpose, allocation_type, allocation_id, project_id,
    email_resource_id, recipient, status, attempts, max_attempts,
    fetched_count, stored_count, matched_count, request_id
) VALUES (
    'OR_MIGRATION_COUNTS', 'order_fetch', 'microsoft', 1, 10,
    100, 'main@example.com', 'running', 2, 3,
    1, 5, 7, 'request-counts'
)`).Error)

	require.NoError(t, goose.UpTo(sqlDB, mailmatchMigrationsDir(t), 33))
	var state struct {
		Status       string `gorm:"column:status"`
		Generation   uint64 `gorm:"column:generation"`
		Failures     int    `gorm:"column:failures"`
		FetchedCount int    `gorm:"column:fetched_count"`
		StoredCount  int    `gorm:"column:stored_count"`
		MatchedCount int    `gorm:"column:matched_count"`
	}
	require.NoError(t, db.Table("mailmatch_resource_fetch_states").Where("email_resource_id = 100").Take(&state).Error)
	require.Equal(t, "pending", state.Status)
	require.Equal(t, uint64(1), state.Generation)
	require.Zero(t, state.Failures, "legacy execution attempts are not business failures")
	require.Equal(t, 7, state.FetchedCount)
	require.Equal(t, 7, state.StoredCount)
	require.Equal(t, 7, state.MatchedCount)
	require.Error(t, db.Table("mailmatch_resource_fetch_states").Where("email_resource_id = 100").Update("stored_count", 8).Error)
}

func TestMailmatchFetchStateMigrationPreservesAdminIdempotencyMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailmatchMigrationsDir(t), 32))
	seedMailmatchOrder(t, db, "OR_MIGRATION_IDEMPOTENCY")

	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_jobs(
    resource_id, operator_user_id, expected_credential_revision, recipient,
    status, attempts, max_attempts, since_at, request_id, path
) VALUES (
    100, 3, 1, 'main@example.com',
    'queued', 2, 3, CURRENT_TIMESTAMP(3), 'admin-request', '/admin/fetch'
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_requests(
    operator_user_id, idempotency_key, resource_id, job_id
)
SELECT 3, 'admin-idempotency', 100, id
FROM mailmatch_resource_fetch_jobs
WHERE resource_id = 100`).Error)

	require.NoError(t, goose.UpTo(sqlDB, mailmatchMigrationsDir(t), 33))
	var state struct {
		OperationKind  string `gorm:"column:operation_kind"`
		IdempotencyKey string `gorm:"column:idempotency_key"`
		Failures       int    `gorm:"column:failures"`
	}
	require.NoError(t, db.Table("mailmatch_resource_fetch_states").Where("email_resource_id = 100").Take(&state).Error)
	require.Equal(t, "resource_fetch", state.OperationKind)
	require.Equal(t, "admin-idempotency", state.IdempotencyKey)
	require.Zero(t, state.Failures, "legacy execution attempts are not business failures")
}
