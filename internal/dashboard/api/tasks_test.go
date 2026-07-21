package api

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestRankingRefreshUsesBackgroundQueueAndDeduplicates(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	enqueueRankingRefresh(context.Background(), client)
	enqueueRankingRefresh(context.Background(), client)

	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })
	tasks, err := inspector.ListPendingTasks(platform.QueueBackgroundInventory)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, typeRankingRefresh, tasks[0].Type)
	require.Equal(t, rankingRefreshTaskTimeout, tasks[0].Timeout)
}
