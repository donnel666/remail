package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/openapi/domain"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyDefaultConcurrency(t *testing.T) {
	require.Equal(t, 500, effectiveAPIKeyConcurrency(nil, 0))
	require.Equal(t, 3, effectiveAPIKeyConcurrency(nil, 3))

	keyLimit := 10
	require.Equal(t, 3, effectiveAPIKeyConcurrency(&keyLimit, 3))
	require.Equal(t, 10, effectiveAPIKeyConcurrency(&keyLimit, 20))

	zero := 0
	require.False(t, validAPIKeyConcurrency(zero))
}

func TestAPIKeyRuntimeAppliesUserGroupConcurrency(t *testing.T) {
	ctx := context.Background()
	keyLimit := 5
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{ID: 1, UserID: 2, KeyPlain: "rk-test", Enabled: true, ConcurrencyLimit: &keyLimit})
	repo.groupConcurrencyLimit = 1
	rt := newAPIKeyRuntime(repo, time.Now)
	defer func() { require.NoError(t, rt.close(ctx)) }()

	_, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	_, err = rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyConcurrencyLimit)
	rt.finish(1)
}

func TestAPIKeyRuntimeUsesDefaultConcurrency(t *testing.T) {
	ctx := context.Background()
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{ID: 1, UserID: 2, KeyPlain: "rk-test", Enabled: true})
	rt := newAPIKeyRuntime(repo, time.Now)
	defer func() { require.NoError(t, rt.close(ctx)) }()

	for range 500 {
		_, err := rt.begin(ctx, "rk-test")
		require.NoError(t, err)
	}
	_, err := rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyConcurrencyLimit)
	for range 500 {
		rt.finish(1)
	}
}

func TestAPIKeyRuntimeConcurrencyAndFlush(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	concurrency := 1
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{
		ID:               1,
		UserID:           2,
		OwnerRole:        "user",
		KeyPlain:         "rk-test",
		Enabled:          true,
		ConcurrencyLimit: &concurrency,
	})
	rt := newAPIKeyRuntime(repo, func() time.Time { return now })
	defer func() {
		require.NoError(t, rt.close(ctx))
	}()

	first, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	require.EqualValues(t, 1, first.ActiveRequests)
	require.EqualValues(t, 1, first.QuotaUsed)
	active, rpm, err := rt.realtimeUsage(ctx, 2)
	require.NoError(t, err)
	require.EqualValues(t, 1, active)
	require.EqualValues(t, 1, rpm)

	_, err = rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyConcurrencyLimit)

	rt.finish(1)
	second, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	require.EqualValues(t, 1, second.ActiveRequests)
	rt.finish(1)
	active, rpm, err = rt.realtimeUsage(ctx, 2)
	require.NoError(t, err)
	require.Zero(t, active)
	require.EqualValues(t, 2, rpm)

	require.NoError(t, rt.flush(ctx))
	require.EqualValues(t, 2, repo.quotaAdded)
	require.EqualValues(t, 2, repo.key.QuotaUsed)
	now = now.Add(apiKeyRPMWindow + time.Second)
	_, rpm, err = rt.realtimeUsage(ctx, 2)
	require.NoError(t, err)
	require.Zero(t, rpm)
}

func TestAPIKeyRuntimeUpdatePreservesActiveRequests(t *testing.T) {
	ctx := context.Background()
	concurrency := 1
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{ID: 1, UserID: 2, KeyPlain: "rk-test", Enabled: true, ConcurrencyLimit: &concurrency})
	uc := NewUseCase(repo)
	defer func() { require.NoError(t, uc.Close(ctx)) }()

	first, err := uc.BeginAPIKeyRequest(ctx, "rk-test")
	require.NoError(t, err)
	_, err = uc.UpdateAPIKey(ctx, UpdateAPIKeyRequest{UserID: 2, KeyID: 1, ConcurrencySet: true, ConcurrencyLimit: &concurrency})
	require.NoError(t, err)
	_, err = uc.BeginAPIKeyRequest(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyConcurrencyLimit)
	require.NoError(t, uc.FinishAPIKeyRequest(ctx, first.UserID, first.APIKeyID, first.LeaseID))
	second, err := uc.BeginAPIKeyRequest(ctx, "rk-test")
	require.NoError(t, err)
	require.NoError(t, uc.FinishAPIKeyRequest(ctx, second.UserID, second.APIKeyID, second.LeaseID))
}

func TestAPIKeyRuntimeReleasesUserLeaseAfterKeyDeletion(t *testing.T) {
	ctx := context.Background()
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{ID: 1, UserID: 2, KeyPlain: "rk-test", Enabled: true})
	gate := &apiKeyConcurrencyGateStub{leases: map[string]uint{}}
	uc := NewUseCase(repo, gate)
	defer func() { require.NoError(t, uc.Close(ctx)) }()

	request, err := uc.BeginAPIKeyRequest(ctx, "rk-test")
	require.NoError(t, err)
	usage, err := uc.GetAPIKeyRealtimeUsage(ctx, request.UserID)
	require.NoError(t, err)
	require.EqualValues(t, 1, usage.ActiveRequests)

	require.NoError(t, uc.DeleteAPIKey(ctx, request.UserID, request.APIKeyID))
	require.NoError(t, uc.FinishAPIKeyRequest(ctx, request.UserID, request.APIKeyID, request.LeaseID))
	usage, err = uc.GetAPIKeyRealtimeUsage(ctx, request.UserID)
	require.NoError(t, err)
	require.Zero(t, usage.ActiveRequests)
}

func TestGetAPIKeyUsageDoesNotDependOnRealtimeStore(t *testing.T) {
	ctx := context.Background()
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{})
	repo.usage = &APIKeyUsage{RequestCount: 7, KeyCount: 2}
	gate := &apiKeyConcurrencyGateStub{realtimeErr: errors.New("redis unavailable")}
	uc := NewUseCase(repo, gate)
	defer func() { require.NoError(t, uc.Close(ctx)) }()

	usage, err := uc.GetAPIKeyUsage(ctx, 2)
	require.NoError(t, err)
	require.EqualValues(t, 7, usage.RequestCount)
	require.EqualValues(t, 2, usage.KeyCount)
	require.Zero(t, gate.realtimeCalls)
}

func TestAPIKeyRuntimeQuota(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	quotaLimit := int64(2)
	concurrency := 5
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{
		ID:               1,
		UserID:           2,
		OwnerRole:        "user",
		KeyPlain:         "rk-test",
		Enabled:          true,
		ConcurrencyLimit: &concurrency,
		QuotaLimit:       &quotaLimit,
	})
	rt := newAPIKeyRuntime(repo, func() time.Time { return now })
	defer func() {
		require.NoError(t, rt.close(ctx))
	}()

	_, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	rt.finish(1)

	_, err = rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	rt.finish(1)

	_, err = rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyQuotaExceeded)
}

func TestAPIKeyRuntimeRejectsCachedKeyAfterOwnerDeletion(t *testing.T) {
	ctx := context.Background()
	concurrency := 1
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{
		ID:               1,
		UserID:           2,
		KeyPlain:         "rk-test",
		Enabled:          true,
		ConcurrencyLimit: &concurrency,
	})
	rt := newAPIKeyRuntime(repo, time.Now)
	defer func() { require.NoError(t, rt.close(ctx)) }()

	_, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	rt.finish(1)
	repo.userActive = false

	_, err = rt.begin(ctx, "rk-test")
	require.ErrorIs(t, err, domain.ErrAPIKeyDisabled)
}

func TestAPIKeyRuntimeUsesCurrentOwnerRoleForCachedKey(t *testing.T) {
	ctx := context.Background()
	concurrency := 1
	repo := newAPIKeyRuntimeRepoStub(domain.APIKey{
		ID:               1,
		UserID:           2,
		OwnerRole:        "supplier",
		KeyPlain:         "rk-test",
		Enabled:          true,
		ConcurrencyLimit: &concurrency,
	})
	rt := newAPIKeyRuntime(repo, time.Now)
	defer func() { require.NoError(t, rt.close(ctx)) }()

	key, err := rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	require.Equal(t, "supplier", key.OwnerRole)
	rt.finish(1)

	repo.ownerRole = "user"
	key, err = rt.begin(ctx, "rk-test")
	require.NoError(t, err)
	require.Equal(t, "user", key.OwnerRole)
	rt.finish(1)
}

type apiKeyRuntimeRepoStub struct {
	key                   domain.APIKey
	usage                 *APIKeyUsage
	quotaAdded            int64
	userActive            bool
	ownerRole             string
	groupConcurrencyLimit int64
}

type apiKeyConcurrencyGateStub struct {
	active        int64
	rpm           int64
	leases        map[string]uint
	realtimeCalls int
	realtimeErr   error
}

func (g *apiKeyConcurrencyGateStub) Acquire(_ context.Context, userID, _ uint, _ int, leaseID string) (int, bool, error) {
	g.active++
	g.rpm++
	g.leases[leaseID] = userID
	return int(g.active), true, nil
}

func (g *apiKeyConcurrencyGateStub) Release(_ context.Context, userID, _ uint, leaseID string) error {
	if g.leases[leaseID] == userID {
		delete(g.leases, leaseID)
		g.active--
	}
	return nil
}

func (g *apiKeyConcurrencyGateStub) RealtimeUsage(context.Context, uint) (int64, int64, error) {
	g.realtimeCalls++
	return g.active, g.rpm, g.realtimeErr
}

func newAPIKeyRuntimeRepoStub(key domain.APIKey) *apiKeyRuntimeRepoStub {
	return &apiKeyRuntimeRepoStub{key: key, userActive: true, ownerRole: key.OwnerRole}
}

func (r *apiKeyRuntimeRepoStub) CreateAPIKey(context.Context, CreateAPIKeyCommand) (*domain.APIKey, bool, error) {
	return nil, false, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) ListAPIKeys(context.Context, uint, int, int) ([]domain.APIKey, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) GetAPIKeyUsage(context.Context, uint) (*APIKeyUsage, error) {
	if r.usage == nil {
		return nil, errors.New("not implemented")
	}
	usage := *r.usage
	return &usage, nil
}

func (r *apiKeyRuntimeRepoStub) FindAPIKey(context.Context, uint, uint) (*domain.APIKey, error) {
	return nil, errors.New("not implemented")
}

func (r *apiKeyRuntimeRepoStub) UpdateAPIKey(_ context.Context, cmd UpdateAPIKeyCommand) (*domain.APIKey, error) {
	if cmd.KeyID != r.key.ID || cmd.UserID != r.key.UserID {
		return nil, domain.ErrAPIKeyNotFound
	}
	if cmd.ConcurrencySet {
		r.key.ConcurrencyLimit = cmd.ConcurrencyLimit
	}
	keyCopy := r.key
	return &keyCopy, nil
}

func (r *apiKeyRuntimeRepoStub) DeleteAPIKey(context.Context, uint, uint, time.Time) error {
	return nil
}

func (r *apiKeyRuntimeRepoStub) FindAPIKeyByPlain(_ context.Context, plain string) (*domain.APIKey, error) {
	if plain != r.key.KeyPlain {
		return nil, domain.ErrAPIKeyNotFound
	}
	keyCopy := r.key
	return &keyCopy, nil
}

func (r *apiKeyRuntimeRepoStub) GetAPIKeyOwnerAccess(context.Context, uint) (string, bool, int64, error) {
	return r.ownerRole, r.userActive, r.groupConcurrencyLimit, nil
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

func (r *apiKeyRuntimeRepoStub) FindOrderTokensByOrders(context.Context, []string) (map[string]domain.OrderToken, error) {
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
