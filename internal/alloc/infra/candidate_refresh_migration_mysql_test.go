package infra

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestCandidateRefreshStateMigrationRoundTripPreservesActiveWorkMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, allocMigrationsDir(t), 27))

	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, role)
VALUES (990281, 'candidate-migration@test.local', 'hash', 'admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status)
VALUES (990282, 'candidate-migration', 'outlook', 'listed')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_candidate_refresh_jobs(
    project_id, operator_user_id, status, affected, attempts, max_attempts,
    last_safe_error, request_id, path
) VALUES (
    990282, 990281, 'running', 14, 1, 1,
    'legacy retry', 'candidate-request', '/candidate'
)`).Error)

	require.NoError(t, goose.UpTo(sqlDB, allocMigrationsDir(t), 28))
	require.False(t, db.Migrator().HasTable("allocation_candidate_refresh_jobs"))
	var state struct {
		Status     string `gorm:"column:candidate_refresh_status"`
		Generation uint64 `gorm:"column:candidate_refresh_generation"`
		Failures   int    `gorm:"column:candidate_refresh_failures"`
		Affected   int    `gorm:"column:candidate_refresh_affected"`
		OperatorID *uint  `gorm:"column:candidate_refresh_operator_user_id"`
		RequestID  string `gorm:"column:candidate_refresh_request_id"`
	}
	require.NoError(t, db.Table("projects").Where("id = 990282").Take(&state).Error)
	require.Equal(t, "pending", state.Status)
	require.Equal(t, uint64(1), state.Generation)
	require.Zero(t, state.Failures, "legacy execution attempts are not business failures")
	require.Equal(t, 14, state.Affected)
	require.NotNil(t, state.OperatorID)
	require.Equal(t, uint(990281), *state.OperatorID)
	require.Equal(t, "candidate-request", state.RequestID)

	require.NoError(t, db.Table("projects").Where("id = 990282").Updates(map[string]any{
		"candidate_refresh_status":     "processing",
		"candidate_refresh_generation": 7,
		"candidate_refresh_failures":   2,
	}).Error)
	require.NoError(t, goose.DownTo(sqlDB, allocMigrationsDir(t), 27))
	var job struct {
		Status      string `gorm:"column:status"`
		Attempts    int    `gorm:"column:attempts"`
		MaxAttempts int    `gorm:"column:max_attempts"`
		RequestID   string `gorm:"column:request_id"`
	}
	require.NoError(t, db.Table("allocation_candidate_refresh_jobs").Where("project_id = 990282").Take(&job).Error)
	require.Equal(t, "pending", job.Status)
	require.Zero(t, job.Attempts)
	require.Equal(t, 1, job.MaxAttempts)
	require.Equal(t, "candidate-request", job.RequestID)
}
