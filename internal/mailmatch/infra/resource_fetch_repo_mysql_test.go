package infra

import (
	"context"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type resourceFetchCredentialStub struct {
	coreapp.MicrosoftCredentialPort
	scope   coreapp.MicrosoftCredentialScope
	rotated coreapp.MicrosoftFetchRefreshTokenRotation
}

func (s *resourceFetchCredentialStub) LockMicrosoftCredentialScope(context.Context, uint) (*coreapp.MicrosoftCredentialScope, error) {
	scope := s.scope
	return &scope, nil
}

func (s *resourceFetchCredentialStub) ApplyMicrosoftFetchRefreshToken(_ context.Context, update coreapp.MicrosoftFetchRefreshTokenRotation) error {
	s.rotated = update
	return nil
}

func TestAdminResourceFetchRepoUsesCurrentResourceStateAndGenerationFenceMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	require.False(t, db.Migrator().HasTable("mailmatch_fetch_jobs"))
	require.False(t, db.Migrator().HasTable("mailmatch_resource_fetch_jobs"))
	require.False(t, db.Migrator().HasTable("mailmatch_resource_fetch_requests"))
	require.False(t, db.Migrator().HasTable("mailmatch_project_history_scan_jobs"))
	seedMailmatchFetchResource(t, db)
	credentials := &resourceFetchCredentialStub{scope: coreapp.MicrosoftCredentialScope{
		ResourceID: 100, Status: "normal", EmailAddress: "main@example.com",
		ClientID: "client", RefreshToken: "rt", CredentialRevision: 7,
	}}
	repo := NewAdminResourceFetchRepo(db)
	repo.SetMicrosoftCredentialPort(credentials)
	ctx := context.Background()

	first := resourceFetchStateRequest("idem-1")
	reused, err := repo.CreateOrReuseResourceFetch(ctx, &first, nil)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, uint64(1), first.Generation)
	require.Equal(t, domain.ResourceFetchJobQueued, first.Status)

	replay := resourceFetchStateRequest("idem-1")
	reused, err = repo.CreateOrReuseResourceFetch(ctx, &replay, nil)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, first.Generation, replay.Generation)

	second := resourceFetchStateRequest("idem-2")
	reused, err = repo.CreateOrReuseResourceFetch(ctx, &second, nil)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, first.Generation+1, second.Generation)

	current, err := repo.MarkResourceFetchProcessing(ctx, 100, first.Generation)
	require.NoError(t, err)
	require.False(t, current)
	current, err = repo.MarkResourceFetchProcessing(ctx, 100, second.Generation)
	require.NoError(t, err)
	require.True(t, current)
	require.ErrorIs(t, repo.AssertResourceFetchFence(ctx, 100, first.Generation, 7), domain.ErrResourceFetchInvalidClaim)
	require.NoError(t, repo.AssertResourceFetchFence(ctx, 100, second.Generation, 7))
}

func TestAdminFetchAndResourceHistoryKeepIndependentStateMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	credentials := &resourceFetchCredentialStub{scope: coreapp.MicrosoftCredentialScope{
		ResourceID: 100, Status: "normal", EmailAddress: "main@example.com",
		ClientID: "client", RefreshToken: "rt", CredentialRevision: 7,
	}}
	adminRepo := NewAdminResourceFetchRepo(db)
	historyRepo := NewResourceHistoryRepo(db)
	adminRepo.SetMicrosoftCredentialPort(credentials)
	historyRepo.SetMicrosoftCredentialPort(credentials)
	ctx := context.Background()

	admin := resourceFetchStateRequest("admin-1")
	history := resourceFetchStateRequest("history-1")
	history.Kind = domain.ResourceFetchJobHistory
	history.SinceAt = nil
	history.UntilAt = nil
	reused, err := adminRepo.CreateOrReuseResourceFetch(ctx, &admin, nil)
	require.NoError(t, err)
	require.False(t, reused)
	reused, err = historyRepo.CreateOrReuseResourceFetch(ctx, &history, nil)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, uint64(1), admin.Generation)
	require.Equal(t, uint64(1), history.Generation)

	claimed, err := adminRepo.MarkResourceFetchProcessing(ctx, 100, admin.Generation)
	require.NoError(t, err)
	require.True(t, claimed)

	var adminState, historyState FetchStateModel
	require.NoError(t, db.Table("mailmatch_admin_resource_fetch_states").First(&adminState, "email_resource_id = ?", 100).Error)
	require.NoError(t, db.Table("mailmatch_resource_fetch_states").First(&historyState, "email_resource_id = ?", 100).Error)
	require.Equal(t, string(domain.ResourceFetchJobRunning), adminState.Status)
	require.Equal(t, string(domain.ResourceFetchJobQueued), historyState.Status)
	require.Equal(t, uint64(1), historyState.Generation)

	adminReplacement := resourceFetchStateRequest("admin-2")
	_, err = adminRepo.CreateOrReuseResourceFetch(ctx, &adminReplacement, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), adminReplacement.Generation)
	require.NoError(t, db.Table("mailmatch_resource_fetch_states").First(&historyState, "email_resource_id = ?", 100).Error)
	require.Equal(t, uint64(1), historyState.Generation)
	require.Equal(t, string(domain.ResourceFetchJobQueued), historyState.Status)
}

func TestResourceFetchStateAcceptsICloudOperationKindMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role)
VALUES (1, 'icloud-owner@test.local', 'hash', 'owner', 'active', 'supplier')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
VALUES (101, 'icloud', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_admin_resource_fetch_states(
    email_resource_id, status, generation, operation_kind
) VALUES (101, 'pending', 1, 'icloud_resource_fetch')`).Error)
}

func TestResourceFetchStateAcceptsGmailOperationKindMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role)
VALUES (1, 'gmail-owner@test.local', 'hash', 'owner', 'active', 'supplier')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
VALUES (102, 'gmail', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_admin_resource_fetch_states(
    email_resource_id, status, generation, operation_kind
) VALUES (102, 'pending', 1, 'gmail_resource_fetch')`).Error)
}

func TestResourceFetchRepoBoundsInfrastructureFailuresMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	credentials := &resourceFetchCredentialStub{scope: coreapp.MicrosoftCredentialScope{
		ResourceID: 100, Status: "normal", EmailAddress: "main@example.com",
		ClientID: "client", RefreshToken: "rt", CredentialRevision: 7,
	}}
	ctx := context.Background()
	for _, kind := range []domain.ResourceFetchJobKind{domain.ResourceFetchJobFetch, domain.ResourceFetchJobHistory} {
		t.Run(string(kind), func(t *testing.T) {
			var repo *ResourceFetchRepo
			stateTable := "mailmatch_admin_resource_fetch_states"
			if kind == domain.ResourceFetchJobHistory {
				historyRepo := NewResourceHistoryRepo(db)
				historyRepo.SetMicrosoftCredentialPort(credentials)
				repo = historyRepo.ResourceFetchRepo
				stateTable = "mailmatch_resource_fetch_states"
			} else {
				adminRepo := NewAdminResourceFetchRepo(db)
				adminRepo.SetMicrosoftCredentialPort(credentials)
				repo = adminRepo.ResourceFetchRepo
			}
			job := resourceFetchStateRequest("idem-failure-" + string(kind))
			job.Kind = kind
			_, err := repo.CreateOrReuseResourceFetch(ctx, &job, nil)
			require.NoError(t, err)

			for attempt := 1; attempt <= 3; attempt++ {
				current, err := repo.MarkResourceFetchProcessing(ctx, 100, job.Generation)
				require.NoError(t, err)
				require.True(t, current)
				retry, err := repo.ReleaseResourceFetchInfrastructureFailure(ctx, 100, job.Generation, "redis unavailable", nil)
				require.NoError(t, err)
				require.Equal(t, attempt < 3, retry)

				var state FetchStateModel
				require.NoError(t, db.Table(stateTable).First(&state, "email_resource_id = ?", 100).Error)
				require.Equal(t, attempt, state.Failures)
				job.Generation = state.Generation
			}

			stored, err := repo.FindResourceFetch(ctx, 100, job.Generation)
			require.NoError(t, err)
			require.Equal(t, kind, stored.Kind)
			require.Equal(t, domain.ResourceFetchJobFailed, stored.Status)
			require.Equal(t, 3, stored.Attempts)
		})
	}
}

func TestResourceFetchRepoCompletesCurrentGenerationAndRotatesTokenMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchFetchResource(t, db)
	credentials := &resourceFetchCredentialStub{scope: coreapp.MicrosoftCredentialScope{
		ResourceID: 100, Status: "normal", EmailAddress: "main@example.com",
		ClientID: "client", RefreshToken: "rt", CredentialRevision: 7,
	}}
	repo := NewAdminResourceFetchRepo(db)
	repo.SetMicrosoftCredentialPort(credentials)
	ctx := context.Background()
	job := resourceFetchStateRequest("idem-complete")
	_, err := repo.CreateOrReuseResourceFetch(ctx, &job, nil)
	require.NoError(t, err)
	current, err := repo.MarkResourceFetchProcessing(ctx, 100, job.Generation)
	require.NoError(t, err)
	require.True(t, current)
	graphAvailable := true

	require.NoError(t, repo.CompleteResourceFetch(
		ctx, 100, job.Generation, 7, "rotated", &graphAvailable, 5, 4, 3, time.Now().UTC(), nil,
	))
	stored, err := repo.FindResourceFetch(ctx, 100, job.Generation)
	require.NoError(t, err)
	require.Equal(t, domain.ResourceFetchJobSucceeded, stored.Status)
	require.Equal(t, 5, stored.FetchedCount)
	require.Equal(t, "rotated", credentials.rotated.RefreshToken)
	require.NotNil(t, credentials.rotated.GraphAvailable)
	require.True(t, *credentials.rotated.GraphAvailable)
}

func resourceFetchStateRequest(idempotencyKey string) domain.ResourceFetchJob {
	now := time.Now().UTC()
	since := now.Add(-time.Hour)
	return domain.ResourceFetchJob{
		Kind: domain.ResourceFetchJobFetch, ResourceID: 100, OperatorUserID: 1,
		Status: domain.ResourceFetchJobQueued, MaxAttempts: 3,
		SinceAt: &since, UntilAt: &now, IdempotencyKey: idempotencyKey,
		RequestID: "request-1", Path: "/admin/resources/100/fetch",
	}
}
