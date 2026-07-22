package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestPickupFetchStateLeaseAndCleanup(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	state := NewPickupFetchState(client)
	ctx := context.Background()

	acquired, err := state.Acquire(ctx, 100, "first", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = state.Acquire(ctx, 100, "second", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)

	require.NoError(t, state.Release(ctx, 100, "second"))
	owned, err := state.Owns(ctx, 100, "first")
	require.NoError(t, err)
	require.True(t, owned)

	extended, err := state.Extend(ctx, 100, "first", 2*time.Minute)
	require.NoError(t, err)
	require.True(t, extended)
	require.NoError(t, state.Release(ctx, 100, "first"))
	acquired, err = state.Acquire(ctx, 100, "second", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	require.NoError(t, state.Release(ctx, 100, "first"))
	owned, err = state.Owns(ctx, 100, "second")
	require.NoError(t, err)
	require.True(t, owned)

	server.FastForward(time.Minute + time.Second)
	acquired, err = state.Acquire(ctx, 100, "third", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
}
