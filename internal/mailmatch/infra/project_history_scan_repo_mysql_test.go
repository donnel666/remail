package infra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectHistoryScanRepoUsesProjectGenerationFenceMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	const projectID = 970017
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status)
VALUES (?, 'history-scan-project', 'outlook', 'listed')`, projectID).Error)

	ctx := context.Background()
	repo := NewProjectHistoryScanRepo(db)
	first, err := repo.RequestProjectHistoryScan(ctx, projectID, "request-1")
	require.NoError(t, err)
	second, err := repo.RequestProjectHistoryScan(ctx, projectID, "request-2")
	require.NoError(t, err)
	require.Equal(t, first.Generation+1, second.Generation)

	pending, err := repo.ListPendingProjectHistoryScans(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, second.Generation, pending[0].Generation)

	current, err := repo.MarkProjectHistoryProcessing(ctx, projectID, first.Generation)
	require.NoError(t, err)
	require.False(t, current, "old generation cannot activate")
	current, err = repo.MarkProjectHistoryProcessing(ctx, projectID, second.Generation)
	require.NoError(t, err)
	require.True(t, current)

	completed, err := repo.CompleteProjectHistoryScan(ctx, projectID, first.Generation, 99, 99, 99)
	require.NoError(t, err)
	require.False(t, completed, "old generation cannot publish results")
	completed, err = repo.CompleteProjectHistoryScan(ctx, projectID, second.Generation, 12, 3, 2)
	require.NoError(t, err)
	require.True(t, completed)

	var saved ProjectHistoryScanStateModel
	require.NoError(t, db.First(&saved, "id = ?", projectID).Error)
	require.Equal(t, "normal", saved.Status)
	require.Equal(t, 12, saved.ScannedCount)
	require.Equal(t, 3, saved.MatchedCount)
	require.Equal(t, 2, saved.SkippedCount)
}

func TestProjectHistoryScanRepoSeparatesInfrastructureAndBusinessFailuresMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	const projectID = 970018
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status)
VALUES (?, 'history-failure-project', 'outlook', 'listed')`, projectID).Error)

	ctx := context.Background()
	repo := NewProjectHistoryScanRepo(db)
	state, err := repo.RequestProjectHistoryScan(ctx, projectID, "request-1")
	require.NoError(t, err)
	current, err := repo.MarkProjectHistoryProcessing(ctx, projectID, state.Generation)
	require.NoError(t, err)
	require.True(t, current)
	released, err := repo.ReleaseProjectHistoryInfrastructureFailure(ctx, projectID, state.Generation, "database unavailable")
	require.NoError(t, err)
	require.True(t, released)
	var releasedState ProjectHistoryScanStateModel
	require.NoError(t, db.First(&releasedState, "id = ?", projectID).Error)
	state.Generation = releasedState.Generation

	for attempt := 1; attempt <= 3; attempt++ {
		current, err = repo.MarkProjectHistoryProcessing(ctx, projectID, state.Generation)
		require.NoError(t, err)
		require.True(t, current)
		recorded, abnormal, err := repo.RecordProjectHistoryFailure(ctx, projectID, state.Generation, "mailbox unavailable")
		require.NoError(t, err)
		require.True(t, recorded)
		require.Equal(t, attempt == 3, abnormal)
	}

	var saved ProjectHistoryScanStateModel
	require.NoError(t, db.First(&saved, "id = ?", projectID).Error)
	require.Equal(t, "abnormal", saved.Status)
	require.Equal(t, 3, saved.Failures)
}
