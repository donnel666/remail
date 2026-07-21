package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestLeaderboardReadsRedisSnapshot(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	todayStart := dashboardapp.TodayStart(time.Date(2026, 7, 21, 0, 42, 0, 0, time.UTC))
	snapshot := leaderboardSnapshot{
		TodayStart: todayStart,
		Today: []dashboardapp.LeaderRow{
			{UserID: 46, Nickname: "first", Count: 858},
			{UserID: 25, Nickname: "second", Count: 450},
			{UserID: 12, Email: "blue9933010@gmail.com", Count: 312},
		},
	}
	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, client.Set(context.Background(), leaderboardCacheKey, payload, leaderboardCacheTTL).Err())

	repo := NewViewRepo(nil, client)
	leaders, err := repo.Leaderboard(context.Background(), &todayStart, 10)
	require.NoError(t, err)
	require.Len(t, leaders, 3)
	require.Equal(t, uint(12), leaders[2].UserID)
	require.Equal(t, 312, leaders[2].Count)

	standing, err := repo.UserStanding(context.Background(), 12, &todayStart)
	require.NoError(t, err)
	require.Equal(t, 3, standing.Rank)
	require.Equal(t, 312, standing.Count)
}
