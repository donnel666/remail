package infra

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type privateInventoryRepoStub struct {
	*inventoryCacheRepoStub
	started chan context.Context
	release chan struct{}
	calls   atomic.Int32
	fail    atomic.Bool
}

func (r *privateInventoryRepoStub) ListPrivateMicrosoftInventoryTotals(ctx context.Context, projectID, viewerUserID uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	r.calls.Add(1)
	r.started <- ctx
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if r.fail.Load() {
		return nil, errors.New("statistics unavailable")
	}
	return []allocapp.PrivateProductInventoryTotal{{ProductID: 20, Suffix: "outlook.com", Available: int64(projectID + viewerUserID)}}, nil
}

func (*privateInventoryRepoStub) ListPrivateDomainInventoryTotals(context.Context, uint, uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	return []allocapp.PrivateProductInventoryTotal{{ProductID: 21, Suffix: "owned.example", Available: 1}}, nil
}

func (*privateInventoryRepoStub) ListPrivateProtoInventoryTotals(context.Context, uint, uint) ([]allocapp.PrivateProductInventoryTotal, error) {
	return []allocapp.PrivateProductInventoryTotal{{ProductID: 24, Suffix: "proton.me", Available: 4}}, nil
}

func TestPrivateInventoryCacheIsAsyncIsolatedAndKeepsSharedSnapshotLive(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	jobs := asynq.NewClient(redisOptions)
	t.Cleanup(func() { _ = jobs.Close() })
	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { _ = inspector.Close() })
	repo := &privateInventoryRepoStub{
		inventoryCacheRepoStub: &inventoryCacheRepoStub{
			privateGmail:  []allocapp.PrivateSingletonInventoryTotal{{ProductID: 22, Available: 2}},
			privateICloud: []allocapp.PrivateSingletonInventoryTotal{{ProductID: 23, Available: 3}},
		},
		started: make(chan context.Context, 8), release: make(chan struct{}),
	}
	cache := NewInventoryCache(client)
	uc := allocapp.NewUseCase(repo, NewInventoryRefreshQueue(jobs))
	uc.SetInventoryCache(cache)
	privateCache := platform.NewHourlySnapshotCache[allocapp.PrivateInventoryTotals](client)
	uc.SetPrivateInventoryCache(privateCache)
	uc.SetProtoProtocolReady(true)
	mux := asynq.NewServeMux()
	mux.HandleFunc(TypePrivateInventoryRefresh, func(ctx context.Context, task *asynq.Task) error {
		var payload PrivateInventoryRefreshPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return err
		}
		return uc.RefreshPrivateInventory(ctx, payload.ProjectID, payload.ViewerUserID)
	})
	worker := asynq.NewServer(redisOptions, asynq.Config{
		Concurrency: 2, Queues: map[string]int{platform.QueueBackgroundInventory: 1},
		TaskCheckInterval: 5 * time.Millisecond, DelayedTaskCheckInterval: 5 * time.Millisecond,
		ShutdownTimeout: time.Second,
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return time.Second },
		IsFailure:       func(err error) bool { return !errors.Is(err, platform.ErrBackgroundExecutionDeferred) },
	})
	require.NoError(t, worker.Start(mux))
	t.Cleanup(worker.Shutdown)
	waitForRetry := func(message string) {
		t.Helper()
		require.Eventually(t, func() bool {
			tasks, err := inspector.ListRetryTasks(platform.QueueBackgroundInventory)
			return err == nil && len(tasks) == 1 && strings.Contains(tasks[0].LastErr, message)
		}, 5*time.Second, time.Millisecond)
	}
	// Wallet/order statistics occupy both process-wide slots. The inventory
	// task must survive admission failure and run when those slots are freed.
	otherCache := platform.NewHourlySnapshotCache[int](client)
	otherStarted := make(chan struct{}, 2)
	otherRelease := make(chan struct{})
	loadOther := func(ctx context.Context) (int, error) {
		otherStarted <- struct{}{}
		select {
		case <-otherRelease:
			return 1, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	otherCache.Get(context.Background(), "test:wallet", loadOther)
	otherCache.Get(context.Background(), "test:orders", loadOther)
	for range 2 {
		select {
		case <-otherStarted:
		case <-time.After(time.Second):
			t.Fatal("background slots were not occupied")
		}
	}
	shared := &allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 4, Items: []allocapp.ProductInventoryTotal{{
		ProductID: 20, ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: 4, PublicAvailable: 4,
		Suffixes: []allocapp.ProductInventorySuffixTotal{{Suffix: "outlook.com", TotalAvailable: 4, PublicAvailable: 4}},
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, cache.RefreshProductInventoryTotals(ctx, 10, shared, 24*time.Hour))
	require.NoError(t, cache.RefreshProductInventoryTotals(ctx, 11, shared, 24*time.Hour))
	start := time.Now()
	cold, err := uc.GetProductInventoryTotals(ctx, 10, 7)
	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second, "cold HTTP reads must not wait for aggregate SQL")
	require.EqualValues(t, 4, cold.TotalAvailable)
	waitForRetry(platform.ErrBackgroundExecutionDeferred.Error())
	require.Zero(t, repo.calls.Load(), "busy capacity must defer SQL and preserve the queued task")
	close(otherRelease)
	var background context.Context
	select {
	case background = <-repo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh did not start")
	}
	cancel()
	require.NoError(t, background.Err(), "request cancellation must not cancel refresh")
	ctx = context.Background()
	for range 5 {
		_, err = uc.GetProductInventoryTotals(ctx, 10, 7)
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, repo.calls.Load(), "concurrent misses share one refresh")
	repo.fail.Store(true)
	close(repo.release)
	waitForRetry("statistics unavailable")
	cached, _, err := privateCache.Read(ctx, "alloc:private-inventory:v1:10:7")
	require.NoError(t, err)
	require.Nil(t, cached, "a failed cold refresh must not publish partial inventory")
	repo.fail.Store(false)
	require.Eventually(t, func() bool {
		_, fresh, err := privateCache.Read(ctx, "alloc:private-inventory:v1:10:7")
		return err == nil && fresh
	}, 5*time.Second, time.Millisecond)
	require.EqualValues(t, 2, repo.calls.Load(), "cold failures recover without another HTTP request")
	waitForTotal := func(projectID, viewerUserID uint, expected int64) {
		t.Helper()
		require.Eventually(t, func() bool {
			totals, readErr := uc.GetProductInventoryTotals(ctx, projectID, viewerUserID)
			return readErr == nil && totals.TotalAvailable == expected
		}, 5*time.Second, time.Millisecond)
	}
	waitForTotal(10, 7, 31)
	totals, err := uc.GetProductInventoryTotals(ctx, 10, 7)
	require.NoError(t, err)
	require.Len(t, totals.Items, 5, "publish every private product type together")
	require.EqualValues(t, 4, totals.Items[0].PublicAvailable)
	public, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 4, public.TotalAvailable, "private data must not enter the shared snapshot")

	other, err := uc.GetProductInventoryTotals(ctx, 10, 8)
	require.NoError(t, err)
	require.EqualValues(t, 4, other.TotalAvailable, "another viewer must not receive the first viewer's totals")
	waitForTotal(10, 8, 32)
	waitForTotal(11, 7, 32)
	require.EqualValues(t, 4, repo.calls.Load(), "different viewers and projects have separate snapshots")
	shared.TotalAvailable = 8
	shared.Items[0].TotalAvailable = 8
	shared.Items[0].PublicAvailable = 8
	shared.Items[0].Suffixes[0].TotalAvailable = 8
	shared.Items[0].Suffixes[0].PublicAvailable = 8
	require.NoError(t, cache.RefreshProductInventoryTotals(ctx, 10, shared, 24*time.Hour))
	waitForTotal(10, 7, 35)
	require.EqualValues(t, 4, repo.calls.Load(), "shared updates must not rerun private SQL")
	require.Eventually(t, func() bool {
		tasks, err := inspector.ListActiveTasks(platform.QueueBackgroundInventory)
		return err == nil && len(tasks) == 0
	}, time.Second, time.Millisecond)

	key := "alloc:private-inventory:v1:10:7"
	payload, err := client.Get(ctx, key).Bytes()
	require.NoError(t, err)
	var stale map[string]any
	require.NoError(t, json.Unmarshal(payload, &stale))
	stale["updatedAt"] = time.Now().Add(-61 * time.Minute)
	payload, err = json.Marshal(stale)
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, key, payload, 24*time.Hour).Err())
	repo.fail.Store(true)
	waitForTotal(10, 7, 35)
	waitForRetry("statistics unavailable")
	waitForTotal(10, 7, 35)
	require.EqualValues(t, 5, repo.calls.Load(), "failed statistics must not retry on every request")
	repo.fail.Store(false)
	// Read only Redis here: recovery must happen without another HTTP request.
	require.Eventually(t, func() bool {
		_, fresh, err := privateCache.Read(ctx, key)
		return err == nil && fresh
	}, 5*time.Second, time.Millisecond)
	require.EqualValues(t, 6, repo.calls.Load(), "the queued failure retries without a one-hour guard")
	require.False(t, server.Exists(key+":refresh"))
	repo.accessErr = errors.New("access revoked")
	_, err = uc.GetProductInventoryTotals(ctx, 10, 7)
	require.ErrorContains(t, err, "access revoked", "cached inventory must still authorize each request")
}
