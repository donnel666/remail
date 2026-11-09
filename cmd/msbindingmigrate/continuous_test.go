package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/stretchr/testify/require"
)

func TestContinuousFailureDoesNotBlockOtherAccountsAndPersistsCooldown(t *testing.T) {
	for _, finite := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuous", true: "finite"}[finite], func(t *testing.T) {
			db := fixture(t, 2)
			ctx := context.Background()
			rows, err := claim(ctx, db, 2, nil, true)
			require.NoError(t, err)
			// Both stale snapshots fail before any network request; the second must still run.
			require.NoError(t, db.Exec("UPDATE email_resources SET version=version+1").Error)
			r := &runtime{db: db, dir: t.TempDir()}
			before := time.Now().UTC()
			require.NoError(t, r.runBatch(ctx, config{dir: r.dir, concurrency: 1, continuous: !finite, continueOnError: finite}, rows))
			for _, id := range []uint{1, 2} {
				var saved item
				require.NoError(t, db.First(&saved, id).Error)
				require.Equal(t, "failed", saved.State)
				require.EqualValues(t, 1, saved.RetryCount)
				require.NotNil(t, saved.RetryAt)
				require.True(t, saved.RetryAt.After(before.Add(23*time.Hour)))
			}
			ready, err := readyWork(ctx, db, before.Add(time.Hour))
			require.NoError(t, err)
			require.Empty(t, ready)
			ready, err = readyWork(ctx, db, before.Add(25*time.Hour))
			require.NoError(t, err)
			require.Len(t, ready, 2)
		})
	}
}

func TestFiniteContinuationSelectsOnlyOriginalSampleWithoutRetryingFailures(t *testing.T) {
	db := fixture(t, 4)
	ctx := context.Background()
	_, err := claim(ctx, db, 4, nil, true)
	require.NoError(t, err)
	require.NoError(t, db.Model(&item{}).Where("resource_id=1").Update("state", "failed").Error)
	require.NoError(t, db.Model(&item{}).Where("resource_id=3").Updates(map[string]any{"state": "mail_pending", "retry_at": time.Now().Add(time.Hour)}).Error)
	rows, err := pendingTrialWork(ctx, db, []uint{1, 2, 3}, time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 2, rows[0].ResourceID)
	_, err = pendingTrialWork(ctx, db, nil, time.Now())
	require.Error(t, err)
	for _, cfg := range []config{
		{limit: 2, ids: []uint{1, 2}},
		{apply: true},
		{apply: true, limit: 1},
		{apply: true, limit: 1, ids: []uint{1}, retryFailed: true},
	} {
		cfg.dir, cfg.concurrency, cfg.continueOnError = t.TempDir(), 1, true
		require.ErrorContains(t, run(ctx, cfg), "explicit finite apply sample")
	}
}

func TestContinuousClaimHoldsChangedAccountAndProcessesTheRest(t *testing.T) {
	db := fixture(t, 2)
	require.NoError(t, db.Table("microsoft_binding_mailboxes").Where("resource_id=1").Update("binding_address", "changed@wsu.edu.kg").Error)
	rows, err := claim(context.Background(), db, 2, nil, true)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 2, rows[0].ResourceID)
	var rejected item
	require.NoError(t, db.First(&rejected, 1).Error)
	require.Equal(t, "failed", rejected.State)
	require.NotNil(t, rejected.RetryAt)
	s, err := loadSnapshot(context.Background(), db, 1, false)
	require.NoError(t, err)
	require.Equal(t, "normal", s.Status)
	require.Equal(t, "changed@wsu.edu.kg", s.BindingAddress)
}

func TestRetryDelayHonorsUpstreamAndKeepsPasswordAttemptsInfrequent(t *testing.T) {
	require.Equal(t, 15*time.Minute, retryDelay(0, errors.New("temporary")))
	require.Equal(t, 30*time.Minute, retryDelay(1, errors.New("temporary")))
	require.Equal(t, 6*time.Hour, retryDelay(100, errors.New("temporary")))
	require.Equal(t, 24*time.Hour, retryDelay(0, &msacl.AuthError{Status: msacl.AuthStatusPasswordError}))
	require.Equal(t, 48*time.Hour, retryDelay(0, &msacl.AuthError{Status: msacl.AuthStatusRateLimited, RetryAfter: 48 * time.Hour}))
}

func TestManualPauseSurvivesRestartAndStopsBeforeRuntimeInitialization(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "PAUSED")
	require.NoError(t, os.WriteFile(path, []byte("manual"), 0600))
	cfg := config{dir: dir, apply: true, continuous: true, concurrency: 1}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, run(ctx, cfg), context.Canceled)
		require.FileExists(t, path)
	}
	require.NoError(t, os.Remove(path))
	require.False(t, manualPauseRequested(dir))
	require.NoError(t, waitForResume(context.Background(), dir))
}

func TestRetrySchemaUpgradePreservesExistingLedger(t *testing.T) {
	db := fixture(t, 1)
	require.NoError(t, db.Exec("ALTER TABLE "+table+" DROP COLUMN retry_at, DROP COLUMN retry_count").Error)
	require.NoError(t, seed(context.Background(), db))
	var saved item
	require.NoError(t, db.First(&saved, 1).Error)
	require.Equal(t, "queued", saved.State)
	require.Zero(t, saved.RetryCount)
	require.Nil(t, saved.RetryAt)
}

func TestStartupRechecksOnlyCredentialsNewerThanMailReceipt(t *testing.T) {
	db := fixture(t, 1)
	ctx := context.Background()
	i, c := prepared(t, db)
	require.NoError(t, commit(ctx, db, i, c))
	s, err := loadSnapshot(ctx, db, 1, false)
	require.NoError(t, err)
	require.NoError(t, commitMailVerification(ctx, db, mailCheckpoint{Snapshot: s, ClientID: s.ClientID, RefreshToken: s.RefreshToken}, mailinfra.MicrosoftMailFetchResult{Valid: true, Protocol: "graph"}))
	count, err := queueMailRechecks(ctx, db)
	require.NoError(t, err)
	require.Zero(t, count)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET credential_revision=credential_revision+1 WHERE id=1").Error)
	count, err = queueMailRechecks(ctx, db)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	var saved item
	require.NoError(t, db.First(&saved, 1).Error)
	require.Equal(t, "mail_pending", saved.State)
	require.Equal(t, i.TargetAddress, saved.TargetAddress)
	require.Nil(t, saved.RetryAt)
}
