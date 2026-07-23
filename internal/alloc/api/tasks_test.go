package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type allocationTaskQueueStub struct {
	candidateCalls atomic.Int32
	inventoryCalls atomic.Int32
}

func (*allocationTaskQueueStub) EnqueueCandidateRefresh(context.Context, allocapp.CandidateRefreshTask) (bool, error) {
	return false, nil
}

func (q *allocationTaskQueueStub) EnqueueCandidateRefreshDispatcher(context.Context, time.Duration) error {
	q.candidateCalls.Add(1)
	return nil
}

func (q *allocationTaskQueueStub) EnqueueInventoryRefresh(context.Context) error {
	q.inventoryCalls.Add(1)
	return nil
}

type allocationBackgroundGateStub struct {
	admitted bool
	released atomic.Bool
}

type failingInventoryTaskRepo struct {
	allocapp.Repository
	calls    atomic.Int32
	deadline time.Time
}

func (r *failingInventoryTaskRepo) GetInventoryStats(ctx context.Context, _ uint) (*allocapp.InventoryStats, error) {
	r.calls.Add(1)
	r.deadline, _ = ctx.Deadline()
	return nil, errors.New("aggregate unavailable")
}

type failingInventoryTaskCache struct {
	allocapp.InventoryCache
}

type boundedInventoryTaskRepo struct {
	allocapp.Repository
	calls atomic.Int32
}

func (r *boundedInventoryTaskRepo) GetInventoryStats(_ context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	r.calls.Add(1)
	return &allocapp.InventoryStats{ProjectID: projectID}, nil
}

type boundedInventoryTaskCache struct {
	allocapp.InventoryCache
	claims int
}

func (c *boundedInventoryTaskCache) ClaimActiveInventory(_ context.Context, _ time.Time, _ time.Time, limit int) ([]allocapp.InventoryCacheEntry, error) {
	c.claims++
	entries := make([]allocapp.InventoryCacheEntry, limit)
	for i := range entries {
		entries[i] = allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheStats, ProjectID: uint((c.claims-1)*limit + i + 1)}
	}
	return entries, nil
}

func (*boundedInventoryTaskCache) AcquireInventoryRefresh(context.Context, allocapp.InventoryCacheEntry, time.Duration) (string, bool, error) {
	return "token", true, nil
}

func (*boundedInventoryTaskCache) RefreshInventoryStats(context.Context, uint, *allocapp.InventoryStats, time.Duration) error {
	return nil
}

func (*boundedInventoryTaskCache) ReleaseInventoryRefresh(context.Context, allocapp.InventoryCacheEntry, string) error {
	return nil
}

func (*failingInventoryTaskCache) ClaimActiveInventory(context.Context, time.Time, time.Time, int) ([]allocapp.InventoryCacheEntry, error) {
	return []allocapp.InventoryCacheEntry{{Kind: allocapp.InventoryCacheStats, ProjectID: 1}}, nil
}

func (*failingInventoryTaskCache) AcquireInventoryRefresh(context.Context, allocapp.InventoryCacheEntry, time.Duration) (string, bool, error) {
	return "token", true, nil
}

func (*failingInventoryTaskCache) ReleaseInventoryRefresh(context.Context, allocapp.InventoryCacheEntry, string) error {
	return nil
}

func (*failingInventoryTaskCache) RequeueInventory(context.Context, []allocapp.InventoryCacheEntry) error {
	return nil
}

func (g *allocationBackgroundGateStub) TryAcquire() (func(), bool) {
	return func() { g.released.Store(true) }, g.admitted
}

func TestInventoryRefreshAdmissionDenialDefersTask(t *testing.T) {
	gate := &allocationBackgroundGateStub{}
	module := &Module{
		UseCase:             allocapp.NewUseCase(nil),
		BackgroundExecution: gate,
	}
	mux := asynq.NewServeMux()
	cleanup := RegisterAllocationTaskHandlers(mux, module)
	t.Cleanup(func() { cleanup(context.Background()) })

	err := mux.ProcessTask(context.Background(), asynq.NewTask(allocinfra.TypeInventoryRefresh, nil))

	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.False(t, gate.released.Load())
}

func TestInventoryRefreshTaskReturnsAggregateFailure(t *testing.T) {
	repo := &failingInventoryTaskRepo{}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(&failingInventoryTaskCache{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	wantDeadline, _ := ctx.Deadline()

	result, deferred, err := refreshInventoryTask(ctx, useCase)

	require.ErrorContains(t, err, "aggregate unavailable")
	require.False(t, deferred)
	require.Equal(t, 1, result.Attempted)
	require.Equal(t, 1, result.Failed)
	require.EqualValues(t, 1, repo.calls.Load())
	require.Equal(t, wantDeadline, repo.deadline, "the Asynq task deadline is the refresh budget")
}

func TestInventoryRefreshTaskStopsNormallyAtItsWorkLimit(t *testing.T) {
	repo := &boundedInventoryTaskRepo{}
	cache := &boundedInventoryTaskCache{}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	result, deferred, err := refreshInventoryTask(context.Background(), useCase)

	require.NoError(t, err)
	require.False(t, deferred)
	require.Equal(t, inventoryRefreshMaxEntriesPerTask, result.Attempted)
	require.Equal(t, inventoryRefreshMaxEntriesPerTask, result.Updated)
	require.EqualValues(t, inventoryRefreshMaxEntriesPerTask, repo.calls.Load())
	require.Equal(t, inventoryRefreshMaxEntriesPerTask/5, cache.claims)
}

func TestAllocationTaskSeedersStopOnCleanup(t *testing.T) {
	queue := &allocationTaskQueueStub{}
	module := &Module{UseCase: allocapp.NewUseCase(nil, queue)}
	cleanup := startAllocationTaskSeeders(module, 2*time.Millisecond, 2*time.Millisecond)
	require.Eventually(t, func() bool {
		return queue.candidateCalls.Load() > 0 && queue.inventoryCalls.Load() > 0
	}, 100*time.Millisecond, time.Millisecond)
	cleanup(context.Background())
	candidateCalls := queue.candidateCalls.Load()
	inventoryCalls := queue.inventoryCalls.Load()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, candidateCalls, queue.candidateCalls.Load())
	require.Equal(t, inventoryCalls, queue.inventoryCalls.Load())
}
