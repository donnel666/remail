package infra

import (
	"context"
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

	active, acquired, err := firstGate.Acquire(ctx, 7, 1, "first")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, active)

	active, acquired, err = secondGate.Acquire(ctx, 7, 1, "second")
	require.NoError(t, err)
	require.False(t, acquired)
	require.Zero(t, active)

	require.NoError(t, secondGate.Release(ctx, 7, "second"))
	_, acquired, err = secondGate.Acquire(ctx, 7, 1, "second")
	require.NoError(t, err)
	require.False(t, acquired)

	require.NoError(t, firstGate.Release(ctx, 7, "first"))
	active, acquired, err = secondGate.Acquire(ctx, 7, 1, "second")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, active)

	server.FastForward(apiKeyConcurrencyLeaseTTL + time.Second)
	active, acquired, err = firstGate.Acquire(ctx, 7, 1, "third")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 1, active)
}
