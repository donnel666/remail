package infra

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestEmailCodeStoreClaimRestoreCommitPreservesTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := NewEmailCodeStore(client)
	ctx := context.Background()

	_, _, err := store.CreateIfAbsent(ctx, "user@test.com", "123456", 600)
	require.NoError(t, err)
	server.FastForward(100 * time.Second)
	ttlBefore := server.TTL(emailCodeRedisKey("user@test.com"))

	claimed, err := store.Claim(ctx, "user@test.com", "wrong", "claim-a")
	require.NoError(t, err)
	require.False(t, claimed)
	code, err := store.Get(ctx, "user@test.com")
	require.NoError(t, err)
	require.Equal(t, "123456", code)

	claimed, err = store.Claim(ctx, "user@test.com", "123456", "claim-a")
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, ttlBefore, server.TTL(emailCodeRedisKey("user@test.com")))
	code, err = store.Get(ctx, "user@test.com")
	require.NoError(t, err)
	require.Equal(t, "123456", code, "claim metadata must never leak as the resend code")

	claimed, err = store.Claim(ctx, "user@test.com", "123456", "claim-b")
	require.NoError(t, err)
	require.False(t, claimed)
	restored, err := store.Restore(ctx, "user@test.com", "claim-b", "123456")
	require.NoError(t, err)
	require.False(t, restored)
	restored, err = store.Restore(ctx, "user@test.com", "claim-a", "123456")
	require.NoError(t, err)
	require.True(t, restored)
	require.Equal(t, ttlBefore, server.TTL(emailCodeRedisKey("user@test.com")))

	claimed, err = store.Claim(ctx, "user@test.com", "123456", "claim-c")
	require.NoError(t, err)
	require.True(t, claimed)
	committed, err := store.Commit(ctx, "user@test.com", "claim-c")
	require.NoError(t, err)
	require.True(t, committed)
	code, err = store.Get(ctx, "user@test.com")
	require.NoError(t, err)
	require.Empty(t, code)
}

func TestEmailCodeStoreClaimConcurrent(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := NewEmailCodeStore(client)
	ctx := context.Background()
	_, _, err := store.CreateIfAbsent(ctx, "user@test.com", "123456", 600)
	require.NoError(t, err)

	const requests = 20
	results := make(chan bool, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, claimErr := store.Claim(ctx, "user@test.com", "123456", fmt.Sprintf("claim-%d", i))
			errs <- claimErr
			results <- claimed
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for claimErr := range errs {
		require.NoError(t, claimErr)
	}
	succeeded := 0
	for claimed := range results {
		if claimed {
			succeeded++
		}
	}
	require.Equal(t, 1, succeeded)
}
