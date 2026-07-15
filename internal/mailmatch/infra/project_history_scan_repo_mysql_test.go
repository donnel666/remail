package infra

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestProjectHistoryScanRepoRecoversPlannerAndCheckpointsRetriesMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	const projectID = 970017
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status)
VALUES (?, 'history-scan-project', 'outlook', 'listed')`, projectID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(id, project_id, type)
VALUES (?, ?, 'microsoft')`, projectID, projectID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled)
VALUES (?, 'recipient', 'exact', 1), (?, 'sender', 'noreply@example\\.com', 1)`, projectID, projectID).Error)

	ctx := context.Background()
	matchRepo := NewRepo(db, nil)
	require.NoError(t, matchRepo.WithTx(ctx, func(txCtx context.Context) error {
		scope, err := matchRepo.FindHistoricalProjectScopeForUpdate(txCtx, projectID)
		require.NoError(t, err)
		require.NotNil(t, scope)
		require.Len(t, scope.Rules, 2)
		return nil
	}))

	repo := NewProjectHistoryScanRepo(db)
	created, err := repo.EnsureMissingPlanners(ctx, 16)
	require.NoError(t, err)
	require.Equal(t, 1, created)
	created, err = repo.EnsureMissingPlanners(ctx, 16)
	require.NoError(t, err)
	require.Zero(t, created)

	planner := claimProjectHistoryJob(t, repo)
	require.Equal(t, -1, planner.Shard)
	require.NoError(t, repo.PlanShards(ctx, planner.ID, planner.ClaimToken, []app.ProjectHistoryScanJob{{
		ProjectID: projectID, Shard: 0, Status: "queued", StartResourceID: 1, EndResourceID: 10, MaxAttempts: 3,
	}}))

	for attempt := 1; attempt <= 3; attempt++ {
		job := claimProjectHistoryJob(t, repo)
		require.Equal(t, 0, job.Shard)
		require.NoError(t, repo.MarkFailure(ctx, job, 7, true, "mailbox unavailable"))

		var saved ProjectHistoryScanJobModel
		require.NoError(t, db.First(&saved, job.ID).Error)
		if attempt < 3 {
			require.Equal(t, uint(0), saved.CheckpointResourceID)
			require.Equal(t, attempt, saved.Attempts)
			continue
		}
		require.Equal(t, uint(7), saved.CheckpointResourceID)
		require.Zero(t, saved.Attempts)
		require.Equal(t, 1, saved.ScannedCount)
		require.Equal(t, 1, saved.SkippedCount)
	}
}

func TestProjectHistoryMigrationMarksExistingMicrosoftProjectsCompleteMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, mailmatchMigrationsDir(t), 16))

	const projectID = 970018
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status)
VALUES (?, 'existing-history-project', 'outlook', 'listed')`, projectID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(id, project_id, type)
VALUES (?, ?, 'microsoft')`, projectID, projectID).Error)
	require.NoError(t, goose.UpTo(sqlDB, mailmatchMigrationsDir(t), 17))

	var marker ProjectHistoryScanJobModel
	require.NoError(t, db.Where("project_id = ? AND shard = -1", projectID).First(&marker).Error)
	require.Equal(t, "succeeded", marker.Status)
	created, err := NewProjectHistoryScanRepo(db).EnsureMissingPlanners(context.Background(), 16)
	require.NoError(t, err)
	require.Zero(t, created)
}

func claimProjectHistoryJob(t *testing.T, repo *ProjectHistoryScanRepo) app.ProjectHistoryScanJob {
	t.Helper()
	now := time.Now().UTC()
	jobs, err := repo.ClaimDispatchable(context.Background(), 1, now.Add(-20*time.Minute), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job, claimed, err := repo.MarkRunning(context.Background(), jobs[0].ID, jobs[0].DispatchToken)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, job)
	return *job
}
