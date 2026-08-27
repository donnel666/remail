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
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type inventoryCacheRepoStub struct {
	allocapp.Repository
	stats         allocapp.InventoryStats
	totals        allocapp.ProjectProductInventoryTotals
	statsCalls    int
	productCalls  int
	accessCalls   int
	accessErr     error
	projectIDs    []uint
	projectCalls  int
	privateGmail  []allocapp.PrivateSingletonInventoryTotal
	privateICloud []allocapp.PrivateSingletonInventoryTotal
}

func (r *inventoryCacheRepoStub) ListInventoryProjectIDs(context.Context) ([]uint, error) {
	r.projectCalls++
	return r.projectIDs, nil
}

func (r *inventoryCacheRepoStub) ListInventoryProjects(context.Context) ([]allocapp.InventoryProject, error) {
	projects := make([]allocapp.InventoryProject, len(r.projectIDs))
	for i, projectID := range r.projectIDs {
		projects[i] = allocapp.InventoryProject{ID: projectID, Name: "project"}
	}
	return projects, nil
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

func (r *inventoryCacheRepoStub) ListPrivateMicrosoftInventoryTotals(context.Context, uint, uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	return nil, nil
}

func (r *inventoryCacheRepoStub) ListPrivateDomainInventoryTotals(context.Context, uint, uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	return nil, nil
}

func (r *inventoryCacheRepoStub) ListPrivateGmailInventoryTotals(context.Context, uint, uint) ([]allocapp.PrivateSingletonInventoryTotal, error) {
	return r.privateGmail, nil
}

func (r *inventoryCacheRepoStub) ListPrivateICloudInventoryTotals(context.Context, uint, uint) ([]allocapp.PrivateSingletonInventoryTotal, error) {
	return r.privateICloud, nil
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

func TestCachedInventoryAllocatorMissSchedulesRefreshWithoutAggregate(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	repo := &inventoryCacheRepoStub{totals: allocapp.ProjectProductInventoryTotals{
		ProjectID: 10,
		Items: []allocapp.ProductInventoryTotal{{
			ProductID: 20,
			Suffixes:  []allocapp.ProductInventorySuffixTotal{{Suffix: "hotmail.com"}},
		}},
	}}
	queue := &inventoryRefreshQueueStub{}
	useCase := allocapp.NewUseCase(repo, queue)
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
	require.NoError(t, cache.RefreshProductInventoryTotals(context.Background(), 10, totals, 24*time.Hour))

	marked, err := useCase.MarkProductInventoryUnavailable(context.Background(), allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: 10, ProductID: 20, EmailSuffix: "@OUTLOOK.COM", PublicOnly: true,
	})
	require.NoError(t, err)
	require.False(t, marked)
	require.Zero(t, repo.productCalls, "allocator misses must not run aggregate SQL")
	require.Equal(t, 1, queue.calls)
	require.Zero(t, client.Exists(context.Background(), productUnavailableMarkerKey(
		allocapp.ProductInventoryAvailabilityRequest{ProjectID: 10, ProductID: 20, EmailSuffix: "outlook.com"},
	)).Val())
	result, err := useCase.RefreshInventoryCacheBefore(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)
	require.Equal(t, 1, repo.productCalls, "the queued due entry must run the aggregate in the worker")

	updated, err := cache.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, updated.TotalAvailable)
	require.Equal(t, "hotmail.com", updated.Items[0].Suffixes[0].Suffix)
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

func TestProductUnavailableCorrectionClearsModeInventory(t *testing.T) {
	code, codePublic := int64(4), int64(3)
	purchase, purchasePublic := int64(5), int64(2)
	totals := &allocapp.ProjectProductInventoryTotals{Items: []allocapp.ProductInventoryTotal{{
		ProductID:     1,
		CodeAvailable: &code, CodePublicAvailable: &codePublic,
		PurchaseAvailable: &purchase, PurchasePublicAvailable: &purchasePublic,
	}}}

	require.True(t, markProductUnavailable(totals, allocapp.ProductInventoryAvailabilityRequest{ProductID: 1, PublicOnly: true}))
	require.Zero(t, *totals.Items[0].CodePublicAvailable)
	require.Zero(t, *totals.Items[0].PurchasePublicAvailable)
	require.True(t, markProductUnavailable(totals, allocapp.ProductInventoryAvailabilityRequest{ProductID: 1}))
	require.Zero(t, *totals.Items[0].CodeAvailable)
	require.Zero(t, *totals.Items[0].PurchaseAvailable)
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

func TestInventoryCacheV8DoesNotServeV7InventorySemantics(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	oldKey := "alloc:inventory:v7:products:10"
	oldActiveKey := "alloc:inventory:v7:active"
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

func TestInventoryCacheBackfillsLegacySingletonModeInventory(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	require.NoError(t, server.Set(inventoryCacheKey(allocapp.InventoryCacheProducts, 10), `{
		"ProjectID":10,
		"TotalAvailable":10,
		"Items":[
			{"ProductID":20,"ProductType":"gmail","TotalAvailable":3,"PublicAvailable":2},
			{"ProductID":21,"ProductType":"icloud","TotalAvailable":7,"PublicAvailable":5}
		]
	}`))

	totals, err := cache.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, totals.Items, 2)
	for _, item := range totals.Items {
		require.NotNil(t, item.CodeAvailable)
		require.NotNil(t, item.CodePublicAvailable)
		require.NotNil(t, item.PurchaseAvailable)
		require.NotNil(t, item.PurchasePublicAvailable)
		require.Equal(t, item.TotalAvailable, *item.CodeAvailable)
		require.Equal(t, item.TotalAvailable, *item.PurchaseAvailable)
		require.Equal(t, item.PublicAvailable, *item.CodePublicAvailable)
		require.Equal(t, item.PublicAvailable, *item.PurchasePublicAvailable)
	}
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

func TestInventoryCacheV8KeysAreProjectScoped(t *testing.T) {
	entry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheStats, ProjectID: 10}
	require.Equal(t, "alloc:inventory:v8:stats:10", inventoryCacheKey(entry.Kind, entry.ProjectID))
	require.Equal(t, "alloc:inventory:v8:lock:stats:10", inventoryCacheLockKey(entry))
	require.Equal(t, "alloc:inventory:v8:active", inventoryCacheScheduleKey)
	require.Equal(t, "alloc:inventory:v8:unavailable:10:20:public:outlook.com", productUnavailableMarkerKey(
		allocapp.ProductInventoryAvailabilityRequest{
			ProjectID: 10, ProductID: 20, EmailSuffix: "@OUTLOOK.COM", PublicOnly: true,
		},
	))

	parsed, ok := parseInventoryCacheKey("alloc:inventory:v8:stats:10")
	require.True(t, ok)
	require.Equal(t, entry, parsed)
	_, ok = parseInventoryCacheKey("alloc:inventory:v8:stats:10:7")
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
	require.False(t, server.Exists("alloc:inventory:v8:products:10:7"))
	require.False(t, server.Exists("alloc:inventory:v8:products:10:8"))
}

func TestColdProductInventoryIncludesOwnedPrivateSingletonModes(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &inventoryCacheRepoStub{
		privateGmail: []allocapp.PrivateSingletonInventoryTotal{
			{ProductID: 20, ProductType: coredomain.ProductTypeGmail, Available: 2},
			{ProductID: 22, ProductType: coredomain.ProductTypeGmailVariant, Available: 1},
		},
		privateICloud: []allocapp.PrivateSingletonInventoryTotal{{ProductID: 21, Available: 3}},
	}
	cache := NewInventoryCache(client)
	require.NoError(t, cache.InitializeInventory(context.Background(), []allocapp.InventoryCacheEntry{{
		Kind: allocapp.InventoryCacheProducts, ProjectID: 10,
	}}, time.Hour))
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	totals, err := useCase.GetProductInventoryTotals(context.Background(), 10, 7)
	require.NoError(t, err)
	require.True(t, totals.Cold)
	require.EqualValues(t, 6, totals.TotalAvailable)
	require.Len(t, totals.Items, 3)
	for index, expected := range []struct {
		productID   uint
		productType coredomain.ProductType
		available   int64
	}{{20, coredomain.ProductTypeGmail, 2}, {22, coredomain.ProductTypeGmailVariant, 1}, {21, coredomain.ProductTypeICloud, 3}} {
		item := totals.Items[index]
		require.Equal(t, expected.productID, item.ProductID)
		require.Equal(t, expected.productType, item.ProductType)
		require.Equal(t, expected.available, item.TotalAvailable)
		require.Zero(t, item.PublicAvailable)
		require.Equal(t, expected.available, *item.CodeAvailable)
		require.Zero(t, *item.CodePublicAvailable)
		require.Equal(t, expected.available, *item.PurchaseAvailable)
		require.Zero(t, *item.PurchasePublicAvailable)
	}
}

func TestGmailProductInventoryTotalsAreSplitBySKU(t *testing.T) {
	stats := &allocapp.InventoryStats{Gmail: allocapp.GmailInventoryStats{
		MainAvailable: 1, DotAvailable: 8, PlusAvailable: 2,
		MainPublicAvailable: 1, DotPublicAvailable: 7, PlusPublicAvailable: 2,
	}}
	main := productInventoryRow{Type: string(coredomain.ProductTypeGmail), PlusWeight: 100}
	require.EqualValues(t, 9, productInventoryTotalFromStats(main, stats))
	require.EqualValues(t, 8, productInventoryPublicTotalFromStats(main, stats))
	variant := productInventoryRow{Type: string(coredomain.ProductTypeGmailVariant), MainWeight: 100, DotWeight: 100}
	require.EqualValues(t, 2, productInventoryTotalFromStats(variant, stats))
	require.EqualValues(t, 2, productInventoryPublicTotalFromStats(variant, stats))
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
	states, err := cache.ListInventoryRefreshStates(context.Background(), []uint{1, 2})
	require.NoError(t, err)
	require.Equal(t, allocapp.InventoryRefreshFailed, states[1].Status)
	require.NotNil(t, states[1].LastAttemptAt)
	require.Contains(t, states[1].LastError, "project one failed")
	require.Nil(t, states[1].NextRefreshAt)
	require.Equal(t, allocapp.InventoryRefreshQueued, states[2].Status)
	require.Nil(t, states[2].NextRefreshAt)
	require.Nil(t, states[2].LastAttemptAt)
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

func TestManualInventoryRefreshQueuesSelectedProjectAndReportsState(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	repo := &inventoryCacheRepoStub{projectIDs: []uint{2, 3}}
	queue := &inventoryRefreshQueueStub{}
	useCase := allocapp.NewUseCase(repo, queue)
	useCase.SetInventoryCache(cache)
	require.NoError(t, cache.RefreshProductInventoryTotals(context.Background(), 2, &allocapp.ProjectProductInventoryTotals{
		ProjectID: 2, TotalAvailable: 26,
	}, allocapp.InventoryRefreshParametersValue().CacheHardTTL))
	require.NoError(t, cache.RecordInventoryRefreshFailure(context.Background(), allocapp.InventoryCacheEntry{
		Kind: allocapp.InventoryCacheProducts, ProjectID: 2,
	}, errors.New("previous refresh failed")))

	projectIDs, err := useCase.TriggerInventoryRefresh(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, []uint{2}, projectIDs)
	require.Equal(t, 1, queue.calls)

	items, err := useCase.ListInventoryRefreshes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, allocapp.InventoryRefreshQueued, items[0].Status)
	require.Equal(t, int64(26), items[0].TotalAvailable)
	require.NotNil(t, items[0].LastRefreshedAt)
	require.Nil(t, items[0].NextRefreshAt)
	require.Nil(t, items[0].LastAttemptAt)
	require.Empty(t, items[0].LastError)
	require.Equal(t, allocapp.InventoryRefreshQueued, items[1].Status)
}

func TestInventoryRefreshStateKeepsStoredTimestampWhenRuntimeTTLChanges(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	now := time.Now().UTC()
	require.NoError(t, cache.RefreshProductInventoryTotals(context.Background(), 10, &allocapp.ProjectProductInventoryTotals{
		ProjectID: 10, TotalAvailable: 4,
	}, time.Hour))
	runtimeconfig.Set("inventory_cache_hard_ttl_hours", "6")
	t.Cleanup(func() { runtimeconfig.Delete("inventory_cache_hard_ttl_hours") })

	states, err := cache.ListInventoryRefreshStates(context.Background(), []uint{10})
	require.NoError(t, err)
	require.WithinDuration(t, now, *states[10].LastRefreshedAt, 2*time.Second)
}

func TestInventoryRefreshRunningStateHasNoNextRefresh(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewInventoryCache(client)
	entry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheProducts, ProjectID: 10}
	require.NoError(t, cache.RefreshProductInventoryTotals(context.Background(), 10, &allocapp.ProjectProductInventoryTotals{
		ProjectID: 10, TotalAvailable: 4,
	}, time.Hour))
	_, acquired, err := cache.AcquireInventoryRefresh(context.Background(), entry, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	states, err := cache.ListInventoryRefreshStates(context.Background(), []uint{10})
	require.NoError(t, err)
	require.Equal(t, allocapp.InventoryRefreshRunning, states[10].Status)
	require.Nil(t, states[10].NextRefreshAt)
}
