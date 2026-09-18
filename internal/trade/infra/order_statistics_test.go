package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOrderStatisticsCacheIsScopedAndDoesNotQueryOnHit(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo := NewRepo(nil, client) // Any SQL on a cache hit fails this test.
	ctx := context.Background()
	mine := tradeapp.OrderListFilter{UserID: 7, Scope: "mine"}
	all := tradeapp.OrderListFilter{UserID: 7, Scope: "all", IsAdmin: true}
	require.NotEqual(t, orderStatisticsKey(mine), orderStatisticsKey(all))
	require.Equal(t, orderStatisticsKey(mine), orderStatisticsKey(tradeapp.OrderListFilter{UserID: 7, Scope: "all"}))
	require.Equal(t, orderStatisticsKey(all), orderStatisticsKey(tradeapp.OrderListFilter{UserID: 8, IsAdmin: true, Scope: "all"}))
	filters := []tradeapp.OrderListFilter{
		{UserID: 8}, {UserID: 7, Status: domain.OrderStatusActive},
		{UserID: 7, ProjectID: 2}, {UserID: 7, Search: "example"},
		{UserID: 7, Domain: "example.com"}, {UserID: 7, ProductType: domain.ProductTypeGmail},
		{UserID: 7, ServiceMode: domain.ServiceModeCode},
		{UserID: 7, CreatedFrom: timePointer(time.Now())},
		{UserID: 7, CreatedTo: timePointer(time.Now())},
	}
	for _, filter := range filters {
		require.NotEqual(t, orderStatisticsKey(mine), orderStatisticsKey(filter))
	}
	for i, filter := range []tradeapp.OrderListFilter{mine, all} {
		key := orderStatisticsKey(filter)
		data := orderStatistics{Total: int64(100 + i), Facets: &tradeapp.OrderListFacets{Status: tradeapp.OrderStatusFacets{All: int64(100 + i)}}}
		repo.stats.Seed(ctx, key, data)
		require.NoError(t, client.Set(ctx, key+":refresh", "1", time.Hour).Err())
		total, facets, err := repo.CachedOrderStats(ctx, filter)
		require.NoError(t, err)
		require.Equal(t, data.Total, total)
		require.Equal(t, data.Facets, facets)
		facets.Status.All = 0
		_, again, err := repo.CachedOrderStats(ctx, filter)
		require.NoError(t, err)
		require.Equal(t, data.Total, again.Status.All, "enrichment must not mutate another request's snapshot")
	}
	// Another process already owns this cold refresh. A request must not
	// bypass the refresh guard and start its own COUNT query (db is nil).
	require.NoError(t, client.Del(ctx, orderStatisticsKey(mine)).Err())
	coldCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	total, facets, err := repo.CachedOrderStats(coldCtx, mine)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Nil(t, facets)
}

func timePointer(value time.Time) *time.Time { return &value }
