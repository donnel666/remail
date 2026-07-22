package infra

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type inventoryCacheRepoStub struct {
	allocapp.Repository
	stats        allocapp.InventoryStats
	totals       allocapp.ProjectProductInventoryTotals
	statsCalls   int
	productCalls int
	accessCalls  int
	accessErr    error
}

type inventoryRefreshQueueStub struct{ calls int }

func (*inventoryRefreshQueueStub) EnqueueCandidateRefresh(context.Context, allocapp.CandidateRefreshTask) (bool, error) {
	return false, nil
}

func (*inventoryRefreshQueueStub) EnqueueCandidateRefreshDispatcher(context.Context, time.Duration) error {
	return nil
}

func (q *inventoryRefreshQueueStub) EnqueueInventoryRefresh(context.Context) error {
	q.calls++
	return nil
}

func (r *inventoryCacheRepoStub) AssertProjectInventoryAccess(context.Context, uint, uint) error {
	r.accessCalls++
	return r.accessErr
}

func (r *inventoryCacheRepoStub) GetInventoryStats(context.Context, uint, uint) (*allocapp.InventoryStats, error) {
	r.statsCalls++
	result := r.stats
	return &result, nil
}

func (r *inventoryCacheRepoStub) GetProductInventoryTotals(context.Context, uint, uint) (*allocapp.ProjectProductInventoryTotals, error) {
	r.productCalls++
	result := r.totals
	return &result, nil
}

func TestInventoryCacheServesRedisAndRefreshesActiveEntries(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{
		stats:  allocapp.InventoryStats{ProjectID: 10, TotalAvailable: 3},
		totals: allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 4},
	}
	queue := &inventoryRefreshQueueStub{}
	useCase := allocapp.NewUseCase(repo, queue)
	useCase.SetInventoryCache(NewInventoryCache(client))

	stats, err := useCase.GetInventoryStats(context.Background(), 10, 7)
	require.ErrorIs(t, err, allocdomain.ErrInventoryRefreshInProgress)
	require.Nil(t, stats)
	require.Zero(t, repo.statsCalls, "HTTP cold misses must not run aggregate SQL")
	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)
	stats, err = useCase.GetInventoryStats(context.Background(), 10, 7)
	require.NoError(t, err)
	require.EqualValues(t, 3, stats.TotalAvailable)
	_, err = useCase.GetInventoryStats(context.Background(), 10, 7)
	require.NoError(t, err)
	require.Equal(t, 1, repo.statsCalls)

	totals, err := useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.ErrorIs(t, err, allocdomain.ErrInventoryRefreshInProgress)
	require.Nil(t, totals)
	require.Zero(t, repo.productCalls, "HTTP cold misses must not run aggregate SQL")
	result, err = useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	totals, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.EqualValues(t, 4, totals.TotalAvailable)
	_, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.Equal(t, 1, repo.productCalls)
	require.Equal(t, 3, repo.accessCalls, "project visibility must be checked on misses and hits")

	repo.stats.TotalAvailable = 8
	repo.totals.TotalAvailable = 9
	server.FastForward(30 * time.Second)
	statsTTL := server.TTL(inventoryCacheKey(allocapp.InventoryCacheStats, 10, 7))
	productsTTL := server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10, 7))
	_, err = useCase.GetInventoryStats(context.Background(), 10, 7)
	require.NoError(t, err)
	_, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.Equal(t, statsTTL, server.TTL(inventoryCacheKey(allocapp.InventoryCacheStats, 10, 7)), "reads must not extend the hard TTL")
	require.Equal(t, productsTTL, server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10, 7)), "reads must not extend the hard TTL")
	result, err = useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	require.Equal(t, 24*time.Hour, server.TTL(inventoryCacheKey(allocapp.InventoryCacheStats, 10, 7)))
	require.Equal(t, 24*time.Hour, server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10, 7)))

	stats, err = useCase.GetInventoryStats(context.Background(), 10, 7)
	require.NoError(t, err)
	require.EqualValues(t, 8, stats.TotalAvailable)
	totals, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.EqualValues(t, 9, totals.TotalAvailable)
	require.Equal(t, 3, repo.statsCalls)
	require.Equal(t, 2, repo.productCalls)
}

func TestInventoryCacheRewarmUpdatesOldActiveMarker(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	repo := &inventoryCacheRepoStub{stats: allocapp.InventoryStats{ProjectID: 10, TotalAvailable: 3}}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)
	key := inventoryCacheKey(allocapp.InventoryCacheStats, 10, 7)
	require.NoError(t, client.ZAdd(context.Background(), inventoryCacheActiveKey, redis.Z{
		Score:  float64(time.Now().Add(-3 * time.Minute).UnixMilli()),
		Member: key,
	}).Err())

	_, err := useCase.GetInventoryStats(context.Background(), 10, 7)
	require.ErrorIs(t, err, allocdomain.ErrInventoryRefreshInProgress)
	require.Zero(t, repo.statsCalls)
	claimed, err := cache.ClaimActiveInventory(context.Background(), time.Now().Add(-2*time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, []allocapp.InventoryCacheEntry{{Kind: allocapp.InventoryCacheStats, ProjectID: 10, BuyerUserID: 7}}, claimed)
}

func TestInventoryCacheChecksAccessBeforeReturningCachedProducts(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{totals: allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 4}}
	cache := NewInventoryCache(client)
	require.NoError(t, cache.SetProductInventoryTotals(context.Background(), 10, 7, &repo.totals, time.Minute))
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	_, err := useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	repo.accessErr = errors.New("access revoked")
	_, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.ErrorContains(t, err, "access revoked")
	require.Zero(t, repo.productCalls)
}

type blockingInventoryRepoStub struct {
	allocapp.Repository
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingInventoryRepoStub) GetInventoryStats(context.Context, uint, uint) (*allocapp.InventoryStats, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	<-r.release
	return &allocapp.InventoryStats{ProjectID: 10, TotalAvailable: 3}, nil
}

func TestInventoryCacheColdMissesReturnImmediatelyWithoutDatabaseWork(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &blockingInventoryRepoStub{started: make(chan struct{}), release: make(chan struct{})}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(NewInventoryCache(client))
	errs := make(chan error, 2)
	go func() {
		_, err := useCase.GetInventoryStats(context.Background(), 10, 7)
		errs <- err
	}()
	go func() {
		_, err := useCase.GetInventoryStats(context.Background(), 10, 7)
		errs <- err
	}()
	require.ErrorIs(t, <-errs, allocdomain.ErrInventoryRefreshInProgress)
	require.ErrorIs(t, <-errs, allocdomain.ErrInventoryRefreshInProgress)
	require.Zero(t, repo.calls.Load())
	require.EqualValues(t, 1, client.ZCard(context.Background(), inventoryCacheActiveKey).Val())
}

type partialInventoryRefreshRepoStub struct {
	allocapp.Repository
}

func (*partialInventoryRefreshRepoStub) GetInventoryStats(_ context.Context, projectID uint, _ uint) (*allocapp.InventoryStats, error) {
	if projectID == 1 {
		return nil, errors.New("project one failed")
	}
	return &allocapp.InventoryStats{ProjectID: projectID, TotalAvailable: 9}, nil
}

func TestInventoryRefreshContinuesAfterOneKeyFails(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	require.NoError(t, cache.SetInventoryStats(context.Background(), 1, 0, &allocapp.InventoryStats{ProjectID: 1}, 5*time.Minute))
	require.NoError(t, cache.SetInventoryStats(context.Background(), 2, 0, &allocapp.InventoryStats{ProjectID: 2}, 5*time.Minute))
	useCase := allocapp.NewUseCase(&partialInventoryRefreshRepoStub{})
	useCase.SetInventoryCache(cache)

	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Attempted)
	require.Equal(t, 1, result.Updated)
	require.Equal(t, 1, result.Failed)
	require.ErrorContains(t, result.LastError, "project one failed")
	stats, err := cache.GetInventoryStats(context.Background(), 2, 0)
	require.NoError(t, err)
	require.EqualValues(t, 9, stats.TotalAvailable)
}

func TestInventoryRefreshBatchIsBounded(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	repo := &partialInventoryRefreshRepoStub{}
	for projectID := uint(2); projectID <= 102; projectID++ {
		require.NoError(t, cache.SetInventoryStats(context.Background(), projectID, 0, &allocapp.InventoryStats{ProjectID: projectID}, 5*time.Minute))
	}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, result.Attempted)
	require.EqualValues(t, 96, client.ZCard(context.Background(), inventoryCacheActiveKey).Val())
}
