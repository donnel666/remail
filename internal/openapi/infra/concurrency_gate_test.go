package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyConcurrencyGateIsSharedAndLeaseOwned(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	firstGate := NewAPIKeyConcurrencyGate(client)
	secondGate := NewAPIKeyConcurrencyGate(client)
	ctx := context.Background()

	active, acquired, err := firstGate.Acquire(ctx, 42, 7, 1, "first")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, active)
	userActive, rpm, err := firstGate.RealtimeUsage(ctx, 42)
	require.NoError(t, err)
	require.EqualValues(t, 1, userActive)
	require.EqualValues(t, 1, rpm)

	active, acquired, err = secondGate.Acquire(ctx, 42, 7, 1, "second")
	require.NoError(t, err)
	require.False(t, acquired)
	require.Zero(t, active)
	_, rpm, err = secondGate.RealtimeUsage(ctx, 42)
	require.NoError(t, err)
	require.EqualValues(t, 1, rpm)

	require.NoError(t, secondGate.Release(ctx, 42, 7, "second"))
	_, acquired, err = secondGate.Acquire(ctx, 42, 7, 1, "second")
	require.NoError(t, err)
	require.False(t, acquired)

	require.NoError(t, firstGate.Release(ctx, 42, 7, "first"))
	active, acquired, err = secondGate.Acquire(ctx, 42, 7, 1, "second")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, active)
	active, acquired, err = firstGate.Acquire(ctx, 42, 8, 1, "other-key")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, active)
	userActive, rpm, err = firstGate.RealtimeUsage(ctx, 42)
	require.NoError(t, err)
	require.EqualValues(t, 2, userActive)
	require.EqualValues(t, 3, rpm)

	server.FastForward(apiKeyConcurrencyLeaseTTL + time.Second)
	active, acquired, err = firstGate.Acquire(ctx, 42, 7, 1, "third")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, active)
	userActive, rpm, err = firstGate.RealtimeUsage(ctx, 42)
	require.NoError(t, err)
	require.EqualValues(t, 1, userActive)
	require.EqualValues(t, 1, rpm)
}

func TestAPIKeyRealtimeUsageExpiresRPMBeforeActiveLease(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	gate := NewAPIKeyConcurrencyGate(client)
	ctx := context.Background()

	_, acquired, err := gate.Acquire(ctx, 42, 7, 1, "lease")
	require.NoError(t, err)
	require.True(t, acquired)

	server.FastForward(apiKeyRPMHashTTL)
	active, rpm, err := gate.RealtimeUsage(ctx, 42)
	require.NoError(t, err)
	require.EqualValues(t, 1, active)
	require.Zero(t, rpm)

	server.FastForward(apiKeyConcurrencyLeaseTTL - apiKeyRPMHashTTL + time.Second)
	active, rpm, err = gate.RealtimeUsage(ctx, 42)
	require.NoError(t, err)
	require.Zero(t, active)
	require.Zero(t, rpm)
}

func TestAPIKeyRPMUsesOneHashSlotPerSecond(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	keys := apiKeyUserUsageKeys(42)
	now := time.Now()

	for i := range 1_000 {
		err := apiKeyUserUsageBeginScript.Run(
			ctx,
			client,
			keys,
			now.UnixMilli(),
			fmt.Sprintf("lease-%d", i),
			now.Add(apiKeyConcurrencyLeaseTTL).UnixMilli(),
			apiKeyConcurrencyLeaseTTL.Milliseconds(),
			now.Unix(),
			apiKeyRPMHashTTL.Milliseconds(),
		).Err()
		require.NoError(t, err)
	}

	require.EqualValues(t, 2, client.HLen(ctx, apiKeyUserRPMKey(42)).Val())
	counts, err := apiKeyUserUsageReadScript.Run(ctx, client, keys, now.UnixMilli(), now.Unix()).Int64Slice()
	require.NoError(t, err)
	require.EqualValues(t, []int64{1_000, 1_000}, counts)
}
