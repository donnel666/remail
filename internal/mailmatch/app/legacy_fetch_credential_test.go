package app

import (
	"context"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type legacyFetchRepoStub struct {
	Repository
	job       domain.FetchJob
	scope     OrderScope
	succeeded bool
}

func (*legacyFetchRepoStub) ClaimFetchJobRunning(context.Context, uint, time.Time) (bool, error) {
	return true, nil
}

func (s *legacyFetchRepoStub) FindFetchJob(context.Context, uint) (*domain.FetchJob, error) {
	job := s.job
	return &job, nil
}

func (s *legacyFetchRepoStub) LoadOrderScopeForServiceToken(context.Context, string) (*OrderScope, error) {
	scope := s.scope
	return &scope, nil
}

func (*legacyFetchRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*legacyFetchRepoStub) UpsertMessages(_ context.Context, messages []domain.Message) ([]domain.Message, error) {
	return messages, nil
}

func (s *legacyFetchRepoStub) MarkFetchJobSucceeded(context.Context, uint, int, int, int, *time.Time, time.Time) error {
	s.succeeded = true
	return nil
}

func (*legacyFetchRepoStub) UpdateFetchStateCompleted(context.Context, uint, uint, string, *time.Time, string, time.Time) error {
	return nil
}

type legacyFetchTransportStub struct {
	result FetchMessagesResult
}

func (s legacyFetchTransportStub) FetchMicrosoftMessages(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) {
	result := s.result
	return &result, nil
}

type legacyFetchCredentialStub struct {
	coreapp.MicrosoftCredentialPort
	update coreapp.MicrosoftFetchRefreshTokenRotation
	err    error
}

func (s *legacyFetchCredentialStub) ApplyMicrosoftFetchRefreshToken(_ context.Context, update coreapp.MicrosoftFetchRefreshTokenRotation) error {
	s.update = update
	return s.err
}

func TestLegacyOrderFetchUsesCredentialRevisionFence(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{
		job: domain.FetchJob{ID: 5, OrderNo: "ORDER-5", EmailResourceID: 91, MaxAttempts: 3, CreatedAt: now},
		scope: OrderScope{
			OrderNo: "ORDER-5", OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 8, EmailResourceID: 91,
			Recipient: "alias@example.com", MicrosoftRT: "old-rt", CredentialRevision: 17,
		},
	}
	credentials := &legacyFetchCredentialStub{}
	uc := NewUseCase(repo, nil, legacyFetchTransportStub{result: FetchMessagesResult{RefreshToken: "rotated-rt"}}, nil)
	uc.SetMicrosoftCredentialPort(credentials)
	uc.now = func() time.Time { return now }

	require.NoError(t, uc.ProcessFetch(context.Background(), FetchTask{JobID: 5}))
	require.True(t, repo.succeeded)
	require.Equal(t, uint(91), credentials.update.ResourceID)
	require.Equal(t, uint64(17), credentials.update.ExpectedCredentialRevision)
	require.Equal(t, "rotated-rt", credentials.update.RefreshToken)
}

func TestLegacyOrderFetchDoesNotOverwriteNewerCredential(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{
		job: domain.FetchJob{ID: 6, OrderNo: "ORDER-6", EmailResourceID: 92, MaxAttempts: 3, CreatedAt: now},
		scope: OrderScope{
			OrderNo: "ORDER-6", OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 9, EmailResourceID: 92,
			Recipient: "alias@example.com", MicrosoftRT: "old-rt", CredentialRevision: 3,
		},
	}
	credentials := &legacyFetchCredentialStub{err: coreapp.ErrMicrosoftCredentialChanged}
	uc := NewUseCase(repo, nil, legacyFetchTransportStub{result: FetchMessagesResult{RefreshToken: "stale-rotated-rt"}}, nil)
	uc.SetMicrosoftCredentialPort(credentials)
	uc.now = func() time.Time { return now }

	require.NoError(t, uc.ProcessFetch(context.Background(), FetchTask{JobID: 6}))
	require.True(t, repo.succeeded, "fetched messages remain usable while the newer RT wins the fence")
}
