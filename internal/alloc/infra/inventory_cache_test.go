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
	projectIDs   []uint
	projectCalls int
}

func (r *inventoryCacheRepoStub) ListInventoryProjectIDs(context.Context) ([]uint, error) {
	r.projectCalls++
	return r.projectIDs, nil
}

type inventoryRefreshQueueStub struct {
	calls int
	err   error
}

func (q *inventoryRefreshQueueStub) EnqueueInventoryRefresh(context.Context) error {
	q.calls++
	return q.err
}

func (q *inventoryRefreshQueueStub) EnqueueInventoryRefreshContinuation(context.Context) error {
	q.calls++
	return q.err
}

func (r *inventoryCacheRepoStub) AssertProjectInventoryAccess(context.Context, uint, uint) error {
	r.accessCalls++
	return r.accessErr
}

func (r *inventoryCacheRepoStub) GetInventoryStats(context.Context, uint) (*allocapp.InventoryStats, error) {
	r.statsCalls++
	result := r.stats
	return &result, nil
}

func (r *inventoryCacheRepoStub) GetProductInventoryTotals(context.Context, uint) (*allocapp.ProjectProductInventoryTotals, error) {
	r.productCalls++
	result := r.totals
	return &result, nil
}

func TestInventoryCacheServesRedisAndRefreshesScheduledEntries(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{
		stats: allocapp.InventoryStats{
			ProjectID: 10, TotalAvailable: 3,
			Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true},
		},
		totals: allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 4},
	}
	queue := &inventoryRefreshQueueStub{}
	useCase := allocapp.NewUseCase(repo, queue)
	useCase.SetInventoryCache(NewInventoryCache(client))

	stats, err := useCase.GetInventoryStats(context.Background(), 10)
	require.ErrorIs(t, err, allocdomain.ErrInventoryRefreshInProgress)
	require.Nil(t, stats)
	require.Zero(t, repo.statsCalls, "HTTP cold misses must not run aggregate SQL")
	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)
	stats, err = useCase.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 3, stats.TotalAvailable)
	_, err = useCase.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, repo.statsCalls)

	totals, err := useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.Equal(t, uint(10), totals.ProjectID)
	require.True(t, totals.Cold)
	require.Zero(t, totals.TotalAvailable)
	require.Zero(t, repo.productCalls, "HTTP cold misses must not run aggregate SQL")
	result, err = useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated, "fresh stats must stay on the backend schedule")
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
	statsTTL := server.TTL(inventoryCacheKey(allocapp.InventoryCacheStats, 10))
	productsTTL := server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10))
	_, err = useCase.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	_, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.Equal(t, statsTTL, server.TTL(inventoryCacheKey(allocapp.InventoryCacheStats, 10)), "reads must not extend the hard TTL")
	require.Equal(t, productsTTL, server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10)), "reads must not extend the hard TTL")
	result, err = useCase.RefreshInventoryCacheBefore(context.Background(), time.Now().Add(allocapp.InventoryRefreshIntervalValue()+time.Second))
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	require.Equal(t, 24*time.Hour, server.TTL(inventoryCacheKey(allocapp.InventoryCacheStats, 10)))
	require.Equal(t, 24*time.Hour, server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10)))

	stats, err = useCase.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 8, stats.TotalAvailable)
	totals, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.EqualValues(t, 9, totals.TotalAvailable)
	require.Equal(t, 2, repo.statsCalls)
	require.Equal(t, 2, repo.productCalls)
}

func TestProductInventorySnapshotsQueueEveryColdProjectBeforeReturning(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{totals: allocapp.ProjectProductInventoryTotals{
		Items: []allocapp.ProductInventoryTotal{{ProductID: 20, TotalAvailable: 4, PublicAvailable: 4}},
	}}
	queue := &inventoryRefreshQueueStub{}
	useCase := allocapp.NewUseCase(repo, queue)
	useCase.SetInventoryCache(NewInventoryCache(client))

	snapshots, err := useCase.GetProductInventorySnapshots(context.Background(), []uint{10, 11})
	require.NoError(t, err)
	require.Len(t, snapshots, 2)
	require.True(t, snapshots[10].Cold)
	require.True(t, snapshots[11].Cold)
	require.Equal(t, 1, queue.calls)
	require.NoError(t, client.ZScore(context.Background(), inventoryCacheScheduleKey, inventoryCacheKey(allocapp.InventoryCacheProducts, 10)).Err())
	require.NoError(t, client.ZScore(context.Background(), inventoryCacheScheduleKey, inventoryCacheKey(allocapp.InventoryCacheProducts, 11)).Err())

	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	snapshots, err = useCase.GetProductInventorySnapshots(context.Background(), []uint{10, 11})
	require.NoError(t, err)
	require.Len(t, snapshots, 2)
	require.Equal(t, 2, repo.productCalls)
}

func TestInitializeInventoryDoesNotOverwriteWarmSnapshots(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	require.NoError(t, cache.SetInventoryStats(context.Background(), 10, &allocapp.InventoryStats{
		ProjectID: 10, TotalAvailable: 7,
	}, time.Hour))
	require.NoError(t, cache.SetProductInventoryTotals(context.Background(), 10, &allocapp.ProjectProductInventoryTotals{
		ProjectID: 10, TotalAvailable: 8,
	}, time.Hour))

	require.NoError(t, cache.InitializeInventory(context.Background(), []allocapp.InventoryCacheEntry{
		{Kind: allocapp.InventoryCacheStats, ProjectID: 10},
		{Kind: allocapp.InventoryCacheProducts, ProjectID: 10},
	}, 24*time.Hour))

	stats, err := cache.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 7, stats.TotalAvailable)
	require.False(t, stats.Cold)
	totals, err := cache.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 8, totals.TotalAvailable)
	require.False(t, totals.Cold)
}

func TestColdInventoryRemainsUnknownWhenImmediateRefreshEnqueueFails(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	queue := &inventoryRefreshQueueStub{err: errors.New("queue unavailable")}
	useCase := allocapp.NewUseCase(&inventoryCacheRepoStub{}, queue)
	useCase.SetInventoryCache(NewInventoryCache(client))

	stats, err := useCase.GetInventoryStats(context.Background(), 10)
	require.ErrorIs(t, err, allocdomain.ErrInventoryRefreshInProgress)
	require.Nil(t, stats)
	snapshots, err := useCase.GetProductInventorySnapshots(context.Background(), []uint{10, 11})
	require.NoError(t, err)
	require.True(t, snapshots[10].Cold)
	require.True(t, snapshots[11].Cold)
	require.Equal(t, 2, queue.calls)

	for _, entry := range []allocapp.InventoryCacheEntry{
		{Kind: allocapp.InventoryCacheStats, ProjectID: 10},
		{Kind: allocapp.InventoryCacheProducts, ProjectID: 10},
		{Kind: allocapp.InventoryCacheProducts, ProjectID: 11},
	} {
		require.NoError(t, client.ZScore(context.Background(), inventoryCacheScheduleKey, inventoryCacheKey(entry.Kind, entry.ProjectID)).Err())
	}
}

func TestCachedInventoryPrecheckAndAllocatorZeroCorrection(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	repo := &inventoryCacheRepoStub{totals: allocapp.ProjectProductInventoryTotals{
		ProjectID: 10, TotalAvailable: 5,
		Items: []allocapp.ProductInventoryTotal{{
			ProductID: 20, TotalAvailable: 5, PublicAvailable: 5,
			Suffixes: []allocapp.ProductInventorySuffixTotal{
				{Suffix: "outlook.com", TotalAvailable: 0, PublicAvailable: 0},
				{Suffix: "hotmail.com", TotalAvailable: 5, PublicAvailable: 5},
			},
		}},
	}}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	totals := &allocapp.ProjectProductInventoryTotals{
		ProjectID:      10,
		TotalAvailable: 12,
		Items: []allocapp.ProductInventoryTotal{{
			ProductID: 20, TotalAvailable: 12, PublicAvailable: 12,
			Suffixes: []allocapp.ProductInventorySuffixTotal{
				{Suffix: "outlook.com", TotalAvailable: 7, PublicAvailable: 7},
				{Suffix: "hotmail.com", TotalAvailable: 5, PublicAvailable: 5},
			},
		}},
	}
	require.NoError(t, cache.SetProductInventoryTotals(context.Background(), 10, totals, 24*time.Hour))

	available, err := useCase.HasProductInventory(context.Background(), allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: 10, ProductID: 20, EmailSuffix: "@OUTLOOK.COM", PublicOnly: true,
	})
	require.NoError(t, err)
	require.True(t, available)

	server.FastForward(time.Hour)
	ttlBefore := server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10))
	marked, err := useCase.MarkProductInventoryUnavailable(context.Background(), allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: 10, ProductID: 20, EmailSuffix: "outlook.com", PublicOnly: true,
	})
	require.NoError(t, err)
	require.True(t, marked)
	require.Equal(t, ttlBefore, server.TTL(inventoryCacheKey(allocapp.InventoryCacheProducts, 10)))
	require.Equal(t, allocapp.InventoryRefreshInterval, server.TTL(productUnavailableMarkerKey(
		allocapp.ProductInventoryAvailabilityRequest{ProjectID: 10, ProductID: 20, EmailSuffix: "outlook.com"},
	)))
	marked, err = useCase.MarkProductInventoryUnavailable(context.Background(), allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: 10, ProductID: 20, EmailSuffix: "outlook.com",
	})
	require.NoError(t, err)
	require.True(t, marked)
	require.Equal(t, 1, repo.productCalls, "a live global correction must suppress duplicate aggregate queries")

	available, err = useCase.HasProductInventory(context.Background(), allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: 10, ProductID: 20, EmailSuffix: "outlook.com", PublicOnly: true,
	})
	require.NoError(t, err)
	require.False(t, available)
	available, err = useCase.HasProductInventory(context.Background(), allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: 10, ProductID: 20, EmailSuffix: "outlook.com",
	})
	require.NoError(t, err)
	require.True(t, available, "the public cache must not reject private-first allocation")

	updated, err := cache.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 5, updated.TotalAvailable)
	require.EqualValues(t, 5, updated.Items[0].TotalAvailable)
	require.EqualValues(t, 5, updated.Items[0].PublicAvailable)
	require.Zero(t, updated.Items[0].Suffixes[0].TotalAvailable)
	require.Zero(t, updated.Items[0].Suffixes[0].PublicAvailable)

	// A background calculation that started before allocator exhaustion must not
	// overwrite the immediate zero correction while its 10-minute marker lives.
	require.NoError(t, cache.RefreshProductInventoryTotals(context.Background(), 10, totals, 24*time.Hour))
	updated, err = cache.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, updated.Items[0].Suffixes[0].TotalAvailable)
	server.FastForward(allocapp.InventoryRefreshInterval + time.Second)
	updated, err = cache.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 7, updated.Items[0].Suffixes[0].TotalAvailable)
}

func TestCachedInventoryPrecheckTreatsColdSnapshotAsKnownZero(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	useCase := allocapp.NewUseCase(&inventoryCacheRepoStub{})
	useCase.SetInventoryCache(NewInventoryCache(client))

	available, err := useCase.HasProductInventory(context.Background(), allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: 10, ProductID: 20,
	})
	require.NoError(t, err)
	require.False(t, available)
	require.EqualValues(t, 1, client.ZCard(context.Background(), inventoryCacheScheduleKey).Val(), "a cold checkout must warm the shared project cache")
}

func TestProductUnavailableCorrectionDoesNotDoubleCountOverlappingProducts(t *testing.T) {
	totals := &allocapp.ProjectProductInventoryTotals{
		TotalAvailable: 21,
		Items: []allocapp.ProductInventoryTotal{
			{ProductID: 1, TotalAvailable: 14, PublicAvailable: 14},
			{ProductID: 2, TotalAvailable: 7, PublicAvailable: 7},
			{ProductID: 3, TotalAvailable: 21, PublicAvailable: 21},
		},
	}

	require.True(t, markProductUnavailable(totals, allocapp.ProductInventoryAvailabilityRequest{ProductID: 1}))
	require.EqualValues(t, 7, totals.TotalAvailable)
}

func TestInventoryCacheRewarmKeepsExistingDueSchedule(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	repo := &inventoryCacheRepoStub{stats: allocapp.InventoryStats{
		ProjectID: 10, TotalAvailable: 3,
		Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true},
	}}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)
	key := inventoryCacheKey(allocapp.InventoryCacheStats, 10)
	require.NoError(t, client.ZAdd(context.Background(), inventoryCacheScheduleKey, redis.Z{
		Score:  float64(time.Now().Add(-3 * time.Minute).UnixMilli()),
		Member: key,
	}).Err())

	stats, err := useCase.GetInventoryStats(context.Background(), 10)
	require.ErrorIs(t, err, allocdomain.ErrInventoryRefreshInProgress)
	require.Nil(t, stats)
	require.Zero(t, repo.statsCalls)
	claimed, err := cache.ClaimDueInventory(context.Background(), time.Now(), 10)
	require.NoError(t, err)
	require.Equal(t, []allocapp.InventoryCacheEntry{{Kind: allocapp.InventoryCacheStats, ProjectID: 10}}, claimed)
}

func TestClaimDueInventoryLeavesFutureEntriesScheduled(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	cutoff := time.Now()
	oldEntry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheStats, ProjectID: 10}
	freshEntry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheStats, ProjectID: 11}
	require.NoError(t, client.ZAdd(context.Background(), inventoryCacheScheduleKey,
		redis.Z{Score: float64(cutoff.Add(-time.Minute).UnixMilli()), Member: inventoryCacheKey(oldEntry.Kind, oldEntry.ProjectID)},
		redis.Z{Score: float64(cutoff.Add(time.Second).UnixMilli()), Member: inventoryCacheKey(freshEntry.Kind, freshEntry.ProjectID)},
	).Err())

	claimed, err := cache.ClaimDueInventory(context.Background(), cutoff, 10)
	require.NoError(t, err)
	require.Equal(t, []allocapp.InventoryCacheEntry{oldEntry}, claimed)
	require.NoError(t, client.ZScore(context.Background(), inventoryCacheScheduleKey, inventoryCacheKey(freshEntry.Kind, freshEntry.ProjectID)).Err())
}

func TestInventoryReadsDoNotChangeRefreshSchedule(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	ctx := context.Background()
	now := time.Now()
	queuedAt := float64(now.Add(-time.Minute).UnixMilli())
	statsEntry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheStats, ProjectID: 10}
	productsEntry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheProducts, ProjectID: 11}
	statsKey := inventoryCacheKey(statsEntry.Kind, statsEntry.ProjectID)
	productsKey := inventoryCacheKey(productsEntry.Kind, productsEntry.ProjectID)
	require.NoError(t, server.Set(statsKey, `{"ProjectID":10,"Microsoft":{"Enabled":true},"TotalAvailable":5}`))
	require.NoError(t, server.Set(productsKey, `{"ProjectID":11,"TotalAvailable":0,"Cold":true}`))
	require.NoError(t, client.ZAdd(ctx, inventoryCacheScheduleKey,
		redis.Z{Score: queuedAt, Member: statsKey},
		redis.Z{Score: queuedAt, Member: productsKey},
	).Err())

	_, err := cache.GetInventoryStats(ctx, statsEntry.ProjectID)
	require.NoError(t, err)
	_, err = cache.GetProductInventorySnapshots(ctx, []uint{productsEntry.ProjectID})
	require.NoError(t, err)
	require.Equal(t, queuedAt, client.ZScore(ctx, inventoryCacheScheduleKey, statsKey).Val())
	require.Equal(t, queuedAt, client.ZScore(ctx, inventoryCacheScheduleKey, productsKey).Val())

	claimed, err := cache.ClaimDueInventory(ctx, now, 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []allocapp.InventoryCacheEntry{statsEntry, productsEntry}, claimed)
}

func TestInventoryRefreshDiscoversAndRestoresBackendSchedule(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{
		projectIDs: []uint{10},
		stats: allocapp.InventoryStats{
			ProjectID: 10, TotalAvailable: 3,
			Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true},
		},
		totals: allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 4},
	}
	cache := NewInventoryCache(client)
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)
	ctx := context.Background()

	result, err := useCase.RefreshInventoryCache(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated, "backend discovery must warm both project snapshots without a read")
	require.True(t, server.Exists(inventoryCacheKey(allocapp.InventoryCacheStats, 10)))
	require.True(t, server.Exists(inventoryCacheKey(allocapp.InventoryCacheProducts, 10)))

	require.NoError(t, client.ZRem(ctx, inventoryCacheScheduleKey,
		inventoryCacheKey(allocapp.InventoryCacheStats, 10),
		inventoryCacheKey(allocapp.InventoryCacheProducts, 10),
	).Err())
	result, err = useCase.RefreshInventoryCache(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated, "backend discovery must restore lost schedule entries")
	require.Equal(t, 2, repo.projectCalls)
}

func TestInventoryCacheV5DoesNotServeV4InventorySemantics(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	oldKey := "alloc:inventory:v4:products:10"
	oldActiveKey := "alloc:inventory:v4:active"
	require.NoError(t, server.Set(oldKey, `{"ProjectID":10,"TotalAvailable":21,"Items":[{"ProductID":20,"TotalAvailable":21,"Suffixes":[{"Suffix":"outlook.com","TotalAvailable":14}]}]}`))
	require.NoError(t, client.ZAdd(context.Background(), oldActiveKey, redis.Z{
		Score: float64(time.Now().UnixMilli()), Member: oldKey,
	}).Err())

	totals, err := cache.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Nil(t, totals)
	claimed, err := cache.ClaimDueInventory(context.Background(), time.Now(), 10)
	require.NoError(t, err)
	require.Empty(t, claimed)
	scheduled, err := client.ZCard(context.Background(), inventoryCacheScheduleKey).Result()
	require.NoError(t, err)
	require.Zero(t, scheduled)
	require.EqualValues(t, 1, client.ZCard(context.Background(), oldActiveKey).Val())
}

func TestInventoryCacheTreatsLegacyColdStatsAsUnknown(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, server.Set(inventoryCacheKey(allocapp.InventoryCacheStats, 10), `{"ProjectID":10,"TotalAvailable":0}`))
	queue := &inventoryRefreshQueueStub{}
	useCase := allocapp.NewUseCase(&inventoryCacheRepoStub{}, queue)
	useCase.SetInventoryCache(NewInventoryCache(client))

	stats, err := useCase.GetInventoryStats(context.Background(), 10)
	require.ErrorIs(t, err, allocdomain.ErrInventoryRefreshInProgress)
	require.Nil(t, stats)
	require.Equal(t, 1, queue.calls)
}

func TestInventoryCacheAcceptsGmailOnlyStats(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	require.NoError(t, cache.SetInventoryStats(context.Background(), 10, &allocapp.InventoryStats{
		ProjectID: 10,
		Gmail:     allocapp.GmailInventoryStats{Enabled: true},
	}, time.Hour))
	queue := &inventoryRefreshQueueStub{}
	useCase := allocapp.NewUseCase(&inventoryCacheRepoStub{}, queue)
	useCase.SetInventoryCache(cache)

	stats, err := useCase.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.True(t, stats.Gmail.Enabled)
	require.Zero(t, queue.calls)
}

func TestInventoryCacheV5KeysAreProjectScoped(t *testing.T) {
	entry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheStats, ProjectID: 10}
	require.Equal(t, "alloc:inventory:v5:stats:10", inventoryCacheKey(entry.Kind, entry.ProjectID))
	require.Equal(t, "alloc:inventory:v5:lock:stats:10", inventoryCacheLockKey(entry))
	require.Equal(t, "alloc:inventory:v5:active", inventoryCacheScheduleKey)
	require.Equal(t, "alloc:inventory:v5:unavailable:10:20:public:outlook.com", productUnavailableMarkerKey(
		allocapp.ProductInventoryAvailabilityRequest{
			ProjectID: 10, ProductID: 20, EmailSuffix: "@OUTLOOK.COM", PublicOnly: true,
		},
	))

	parsed, ok := parseInventoryCacheKey("alloc:inventory:v5:stats:10")
	require.True(t, ok)
	require.Equal(t, entry, parsed)
	_, ok = parseInventoryCacheKey("alloc:inventory:v5:stats:10:7")
	require.False(t, ok)
}

func TestInventoryCacheChecksAccessBeforeReturningCachedProducts(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{totals: allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 4}}
	cache := NewInventoryCache(client)
	require.NoError(t, cache.SetProductInventoryTotals(context.Background(), 10, &repo.totals, time.Minute))
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	_, err := useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	repo.accessErr = errors.New("access revoked")
	_, err = useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.ErrorContains(t, err, "access revoked")
	require.Zero(t, repo.productCalls)
}

func TestInventoryCacheSharesOneProjectSnapshotAcrossViewers(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{totals: allocapp.ProjectProductInventoryTotals{
		ProjectID: 10, TotalAvailable: 4,
	}}
	cache := NewInventoryCache(client)
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	cold, err := useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.True(t, cold.Cold)
	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)

	totals, err := useCase.GetProductInventoryTotals(context.Background(), 10, 8)
	require.NoError(t, err)
	require.EqualValues(t, 4, totals.TotalAvailable)
	require.Equal(t, 1, repo.productCalls, "the shared aggregate must run only once")
	require.Equal(t, 2, repo.accessCalls, "each viewer must still be authorized")
	require.True(t, server.Exists(inventoryCacheKey(allocapp.InventoryCacheProducts, 10)))
	require.False(t, server.Exists("alloc:inventory:v5:products:10:7"))
	require.False(t, server.Exists("alloc:inventory:v5:products:10:8"))
}

type blockingInventoryRepoStub struct {
	allocapp.Repository
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingInventoryRepoStub) GetInventoryStats(context.Context, uint) (*allocapp.InventoryStats, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	<-r.release
	return &allocapp.InventoryStats{
		ProjectID: 10, TotalAvailable: 3,
		Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true},
	}, nil
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
		_, err := useCase.GetInventoryStats(context.Background(), 10)
		errs <- err
	}()
	go func() {
		_, err := useCase.GetInventoryStats(context.Background(), 10)
		errs <- err
	}()
	require.ErrorIs(t, <-errs, allocdomain.ErrInventoryRefreshInProgress)
	require.ErrorIs(t, <-errs, allocdomain.ErrInventoryRefreshInProgress)
	require.Zero(t, repo.calls.Load())
	require.EqualValues(t, 1, client.ZCard(context.Background(), inventoryCacheScheduleKey).Val())
}

type partialInventoryRefreshRepoStub struct {
	allocapp.Repository
}

func (*partialInventoryRefreshRepoStub) ListInventoryProjectIDs(context.Context) ([]uint, error) {
	return nil, nil
}

func (*partialInventoryRefreshRepoStub) GetInventoryStats(_ context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	if projectID == 1 {
		return nil, errors.New("project one failed")
	}
	return &allocapp.InventoryStats{
		ProjectID: projectID, TotalAvailable: 9,
		Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true},
	}, nil
}

func TestInventoryRefreshContinuesAfterOneKeyFails(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	require.NoError(t, cache.SetInventoryStats(context.Background(), 1, &allocapp.InventoryStats{ProjectID: 1}, 5*time.Minute))
	require.NoError(t, cache.SetInventoryStats(context.Background(), 2, &allocapp.InventoryStats{ProjectID: 2}, 5*time.Minute))
	useCase := allocapp.NewUseCase(&partialInventoryRefreshRepoStub{})
	useCase.SetInventoryCache(cache)

	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Attempted)
	require.Equal(t, 1, result.Updated)
	require.Equal(t, 1, result.Failed)
	require.ErrorContains(t, result.LastError, "project one failed")
	stats, err := cache.GetInventoryStats(context.Background(), 2)
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
		require.NoError(t, cache.SetInventoryStats(context.Background(), projectID, &allocapp.InventoryStats{ProjectID: projectID}, 5*time.Minute))
	}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, result.Attempted)
	require.EqualValues(t, 101, client.ZCard(context.Background(), inventoryCacheScheduleKey).Val(), "refreshed entries must schedule their next backend refresh")
}
