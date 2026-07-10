package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/openapi/domain"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyRuntimeConcurrencyAndFlush(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{
		ID:               1,
		UserID:           2,
		OwnerRole:        "user",
		KeyPlain:         "rk-test",
		Enabled:          true,
		ConcurrencyLimit: 1,
	})
	rt := newAPIKeyRuntime(repo, func() time.Time { return now })
	defer func() {
		require.NoError(t, rt.close(ctx))
	}()

	first, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	require.EqualValues(t, 1, first.ActiveRequests)
	require.EqualValues(t, 1, first.QuotaUsed)

	_, err = rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyConcurrencyLimit)

	rt.finish(1)
	second, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	require.EqualValues(t, 1, second.ActiveRequests)
	rt.finish(1)

	require.NoError(t, rt.flush(ctx))
	require.EqualValues(t, 2, repo.quotaAdded)
	require.EqualValues(t, 2, repo.key.QuotaUsed)
}

func TestAPIKeyRuntimeRateLimitAndQuota(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rateLimit := 1
	quotaLimit := int64(2)
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{
		ID:                 1,
		UserID:             2,
		OwnerRole:          "user",
		KeyPlain:           "rk-test",
		Enabled:            true,
		ConcurrencyLimit:   5,
		RateLimitPerMinute: &rateLimit,
		QuotaLimit:         &quotaLimit,
	})
	rt := newAPIKeyRuntime(repo, func() time.Time { return now })
	defer func() {
		require.NoError(t, rt.close(ctx))
	}()

	_, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	rt.finish(1)

	_, err = rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyRateLimited)

	now = now.Add(61 * time.Second)
	_, err = rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	rt.finish(1)

	now = now.Add(61 * time.Second)
	_, err = rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyQuotaExceeded)
}

type apiKeyRuntimeRepoStub struct {
	key        domain.APIKey
	quotaAdded int64
}

func newAPIKeyRuntimeRepoStub(key domain.APIKey) *apiKeyRuntimeRepoStub {
	return &apiKeyRuntimeRepoStub{key: key}
}

func (r *apiKeyRuntimeRepoStub) CreateAPIKey(context.Context, CreateAPIKeyCommand) (*domain.APIKey, bool, error) {
	return nil, false, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) ListAPIKeys(context.Context, uint, int, int) ([]domain.APIKey, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) GetAPIKeyUsage(context.Context, uint) (*APIKeyUsage, error) {
	return nil, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) FindAPIKey(context.Context, uint, uint) (*domain.APIKey, error) {
	return nil, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) UpdateAPIKey(context.Context, UpdateAPIKeyCommand) (*domain.APIKey, error) {
	return nil, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) DeleteAPIKey(context.Context, uint, uint, time.Time) error {
	return errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) FindAPIKeyByPlain(_ context.Context, plain string) (*domain.APIKey, error) {
	if plain != r.key.KeyPlain {
		return nil, domain.ErrAPIKeyNotFound
	}
	keyCopy := r.key
	return &keyCopy, nil
}

func (r *apiKeyRuntimeRepoStub) AddAPIKeyQuotaUsed(_ context.Context, keyID uint, delta int64, _ time.Time) error {
	if keyID != r.key.ID {
		return domain.ErrAPIKeyNotFound
	}
	r.quotaAdded += delta
	r.key.QuotaUsed += delta
	return nil
}

func (r *apiKeyRuntimeRepoStub) IssueOrderToken(context.Context, IssueOrderTokenCommand) (*domain.OrderToken, error) {
	return nil, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) FindOrderTokenByOrder(context.Context, string) (*domain.OrderToken, error) {
	return nil, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) FindOrderTokenByPlain(context.Context, string) (*domain.OrderToken, error) {
	return nil, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) ExtendOrderToken(context.Context, string, time.Time) error {
	return errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) DisableOrderToken(context.Context, string, string, time.Time) error {
	return errors.New("not implemented")
}
