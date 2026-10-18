package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestPickupMessageCacheSharesContentByResourceForTenSeconds(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewPickupMessageCache(client)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	messages := []mailmatchapp.FetchedMessage{{
		EmailResourceID: 100, ResourceType: domain.ResourceTypeMicrosoft,
		Recipient: "alias@outlook.com", Recipients: []string{"alias@outlook.com"},
		Sender: "sender@example.com", Subject: "code", Body: "123456",
		RawSource: "full MIME source", ProviderPayload: `{"body":"123456"}`,
		Protocol: "graph", Folder: "Inbox", ReceivedAt: now,
	}}

	require.NoError(t, cache.Store(ctx, 100, messages, 10*time.Second))
	stored, found, err := cache.Load(ctx, 100)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, messages, stored)
	require.NoError(t, cache.Store(ctx, 101, []mailmatchapp.FetchedMessage{}, 10*time.Second))
	batch, err := cache.LoadMany(ctx, []uint{100, 101, 999, 100})
	require.NoError(t, err)
	require.Equal(t, messages, batch[100])
	require.Contains(t, batch, uint(101), "an empty cached mailbox must remain distinguishable from a cache miss")
	require.Empty(t, batch[101])
	require.NotContains(t, batch, uint(999))

	_, found, err = cache.Load(ctx, 102)
	require.NoError(t, err)
	require.False(t, found)

	server.FastForward(10*time.Second + time.Millisecond)
	_, found, err = cache.Load(ctx, 100)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPickupScanProgressSurvivesBodyExpiryAndRequiresCurrentLease(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewPickupMessageCache(client)
	lease := NewPickupFetchState(client)
	ctx := context.Background()
	acquired, err := lease.Acquire(ctx, 91, "owner-one", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	progress := mailmatchapp.PickupScanProgress{ResourceType: domain.ResourceTypeDaum, CredentialRevision: 3, BackfillCursor: "page-7"}
	stored, err := cache.StoreScanProgress(ctx, 91, "owner-one", progress, 24*time.Hour)
	require.NoError(t, err)
	require.True(t, stored)
	require.NoError(t, cache.Store(ctx, 91, []mailmatchapp.FetchedMessage{}, 10*time.Second))
	server.FastForward(11 * time.Second)
	_, found, err := cache.Load(ctx, 91)
	require.NoError(t, err)
	require.False(t, found)
	loaded, err := cache.LoadScanProgress(ctx, 91)
	require.NoError(t, err)
	require.Equal(t, progress, *loaded)
	require.NoError(t, lease.Release(ctx, 91, "owner-one"))
	acquired, err = lease.Acquire(ctx, 91, "owner-two", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	stale := progress
	stale.BackfillCursor = "stale-page"
	stored, err = cache.StoreScanProgress(ctx, 91, "owner-one", stale, 24*time.Hour)
	require.NoError(t, err)
	require.False(t, stored)
	loaded, err = cache.LoadScanProgress(ctx, 91)
	require.NoError(t, err)
	require.Equal(t, progress, *loaded)
}
