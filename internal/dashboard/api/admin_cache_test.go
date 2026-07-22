package api

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAdminDashboardCacheRefreshesActiveRangeFor24Hours(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := newAdminDashboardCache(client)
	ctx := context.Background()
	from := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	require.NoError(t, cache.set(ctx, &from, &to, &dashboardapp.AdminDashboard{Stats: dashboardapp.AdminStats{TotalUsers: 1}}))
	key := adminDashboardCacheKey(&from, &to)
	activityBefore := client.ZScore(ctx, adminDashboardActiveKey, key).Val()
	require.Equal(t, adminDashboardCacheTTL, server.TTL(key))

	require.NoError(t, cache.refresh(ctx, func(context.Context, *time.Time, *time.Time) (*dashboardapp.AdminDashboard, error) {
		return &dashboardapp.AdminDashboard{Stats: dashboardapp.AdminStats{TotalUsers: 2}}, nil
	}))
	require.Equal(t, activityBefore, client.ZScore(ctx, adminDashboardActiveKey, key).Val(), "background refresh must not keep inactive ranges alive")
	require.Equal(t, adminDashboardCacheTTL, server.TTL(key))

	got, ok, err := cache.get(ctx, &from, &to)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, got.Stats.TotalUsers)
}
