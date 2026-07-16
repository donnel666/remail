package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftCredentialServiceMutations(t *testing.T) {
	now := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)

	t.Run("token refresh rotates credentials once and records diagnostics", func(t *testing.T) {
		repo := newMicrosoftCredentialRepositoryStub()
		service := NewMicrosoftCredentialService(repo)

		scope, err := service.LockMicrosoftCredentialScope(context.Background(), 10)
		require.NoError(t, err)
		require.Equal(t, uint64(4), scope.CredentialRevision)
		require.Equal(t, "old-refresh-token", scope.RefreshToken)

		err = service.ApplyMicrosoftTokenRefreshSuccess(context.Background(), MicrosoftTokenRefreshSuccess{
			ResourceID: 10, ExpectedCredentialRevision: 4,
			ClientID: "new-client", RefreshToken: "new-refresh-token",
			RequestID: "request-10", Now: now,
		})
		require.NoError(t, err)
		require.Equal(t, "new-client", repo.resource.ClientID)
		require.Equal(t, "new-refresh-token", repo.resource.RefreshToken)
		require.Equal(t, uint64(5), repo.resource.CredentialRevision)
		require.Equal(t, now, repo.resource.CredentialUpdatedAt)
		require.Equal(t, now, *repo.resource.TokenLastRefreshedAt)
		require.Equal(t, "request-10", repo.resource.TokenLastRequestID)
		require.Empty(t, repo.resource.LastSafeError)
		require.Equal(t, uint64(2), repo.root.Version)
		require.Equal(t, 1, repo.saves)
	})

	t.Run("failure diagnostic does not advance credential revision", func(t *testing.T) {
		repo := newMicrosoftCredentialRepositoryStub()
		service := NewMicrosoftCredentialService(repo)

		err := service.ApplyMicrosoftTokenRefreshFailure(context.Background(), MicrosoftTokenRefreshFailure{
			ResourceID: 10, ExpectedCredentialRevision: 4,
			SafeError: "safe diagnostic", RequestID: "request-failed",
		})
		require.NoError(t, err)
		require.Equal(t, uint64(4), repo.resource.CredentialRevision)
		require.Equal(t, "safe diagnostic", repo.resource.LastSafeError)
		require.Equal(t, "request-failed", repo.resource.TokenLastRequestID)
		require.Equal(t, uint64(2), repo.root.Version)
	})

	t.Run("fetch rotation is fenced and no-ops for the current token", func(t *testing.T) {
		repo := newMicrosoftCredentialRepositoryStub()
		service := NewMicrosoftCredentialService(repo)

		err := service.ApplyMicrosoftFetchRefreshToken(context.Background(), MicrosoftFetchRefreshTokenRotation{
			ResourceID: 10, ExpectedCredentialRevision: 3,
			RefreshToken: "must-not-apply", Now: now,
		})
		require.ErrorIs(t, err, ErrMicrosoftCredentialChanged)
		require.Equal(t, "old-refresh-token", repo.resource.RefreshToken)
		require.Zero(t, repo.saves)

		err = service.ApplyMicrosoftFetchRefreshToken(context.Background(), MicrosoftFetchRefreshTokenRotation{
			ResourceID: 10, ExpectedCredentialRevision: 4,
			RefreshToken: "old-refresh-token", Now: now,
		})
		require.NoError(t, err)
		require.Zero(t, repo.saves)

		err = service.ApplyMicrosoftFetchRefreshToken(context.Background(), MicrosoftFetchRefreshTokenRotation{
			ResourceID: 10, ExpectedCredentialRevision: 4,
			RefreshToken: "fetch-rotated-token", Now: now,
		})
		require.NoError(t, err)
		require.Equal(t, "fetch-rotated-token", repo.resource.RefreshToken)
		require.Equal(t, uint64(5), repo.resource.CredentialRevision)
		require.Equal(t, now, repo.resource.CredentialUpdatedAt)
		require.Equal(t, uint64(2), repo.root.Version)
	})

	t.Run("rotation returns an in-flight validation to pending", func(t *testing.T) {
		repo := newMicrosoftCredentialRepositoryStub()
		repo.resource.Status = domain.MicrosoftStatusValidating
		service := NewMicrosoftCredentialService(repo)

		err := service.ApplyMicrosoftFetchRefreshToken(context.Background(), MicrosoftFetchRefreshTokenRotation{
			ResourceID: 10, ExpectedCredentialRevision: 4,
			RefreshToken: "validation-fenced-token", Now: now,
		})

		require.NoError(t, err)
		require.Equal(t, uint64(5), repo.resource.CredentialRevision)
		require.Equal(t, domain.MicrosoftStatusPending, repo.resource.Status)
	})
}

type microsoftCredentialRepositoryStub struct {
	root     *domain.EmailResource
	resource *domain.MicrosoftResource
	saves    int
}

func newMicrosoftCredentialRepositoryStub() *microsoftCredentialRepositoryStub {
	return &microsoftCredentialRepositoryStub{
		root: &domain.EmailResource{ID: 10, Type: domain.ResourceTypeMicrosoft, OwnerUserID: 1, Version: 1},
		resource: &domain.MicrosoftResource{
			ID: 10, EmailAddress: "mailbox@example.com", Password: "password",
			ClientID: "old-client", RefreshToken: "old-refresh-token",
			CredentialRevision: 4, Status: domain.MicrosoftStatusDisabled,
			LastSafeError: "old diagnostic",
		},
	}
}

func (r *microsoftCredentialRepositoryStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *microsoftCredentialRepositoryStub) LockAdminMicrosoft(_ context.Context, resourceID uint) (*domain.EmailResource, *domain.MicrosoftResource, error) {
	if resourceID != r.root.ID {
		return nil, nil, domain.ErrResourceNotFound
	}
	return r.root, r.resource, nil
}

func (r *microsoftCredentialRepositoryStub) MaxMicrosoftResourceID(context.Context) (uint, error) {
	return r.resource.ID, nil
}

func (r *microsoftCredentialRepositoryStub) FindNextMicrosoft(_ context.Context, afterID, maxID uint) (*domain.MicrosoftResource, error) {
	if r.resource.ID <= afterID || r.resource.ID > maxID {
		return nil, nil
	}
	return r.resource, nil
}

func (r *microsoftCredentialRepositoryStub) SaveAdminMicrosoft(_ context.Context, root *domain.EmailResource, _ *domain.MicrosoftResource, expectedVersion uint64) error {
	if root.Version != expectedVersion {
		return domain.ErrResourceVersionConflict
	}
	r.saves++
	root.Version++
	return nil
}
