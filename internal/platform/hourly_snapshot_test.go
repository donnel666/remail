package platform

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestHourlySnapshotKeepsReadsFastAndRefreshesOnce(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewHourlySnapshotCache[int](client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := "test:hourly"
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	load := func(ctx context.Context) (int, error) {
		calls.Add(1)
		entered <- ctx
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	require.Nil(t, cache.Get(ctx, key, load), "cold reads must not wait for the loader")
	var background context.Context
	select {
	case background = <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	require.NoError(t, background.Err(), "HTTP cancellation must not cancel the shared refresh")
	for range 10 {
		require.Nil(t, cache.Get(context.Background(), key, load))
	}
	require.EqualValues(t, 1, calls.Load())
	close(release)
	require.Eventually(t, func() bool {
		value := cache.Get(context.Background(), key, load)
		return value != nil && *value == 42
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return len(hourlySnapshotSlots) == 0 }, time.Second, time.Millisecond)

	for _, age := range []time.Duration{59 * time.Minute, time.Hour + time.Second} {
		payload, err := json.Marshal(hourlySnapshot[int]{UpdatedAt: time.Now().Add(-age), Data: 42})
		require.NoError(t, err)
		require.NoError(t, client.Set(context.Background(), key, payload, 24*time.Hour).Err())
		server.FastForward(time.Hour)
		value := cache.Get(context.Background(), key, func(context.Context) (int, error) {
			calls.Add(1)
			return 0, errors.New("database unavailable")
		})
		require.NotNil(t, value)
		require.Equal(t, 42, *value)
		if age < time.Hour {
			require.EqualValues(t, 1, calls.Load(), "59 minutes is still fresh")
		} else {
			require.Eventually(t, func() bool { return calls.Load() == 2 && len(hourlySnapshotSlots) == 0 }, time.Second, time.Millisecond)
		}
	}
	value := cache.Get(context.Background(), key, load)
	require.Equal(t, 42, *value, "failed refresh must preserve stale data")
	require.EqualValues(t, 2, calls.Load(), "failure must not cause a retry on every request")
}
