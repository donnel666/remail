package infra

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestInventoryRefreshQueueUsesBackgroundWorker(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	queue := NewInventoryRefreshQueue(client)

	require.NoError(t, queue.EnqueueInventoryRefresh(context.Background()))
	require.NoError(t, queue.EnqueueInventoryRefresh(context.Background()))
	require.NoError(t, queue.EnqueueInventoryRefreshContinuation(context.Background()))

	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })
	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundInventory)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	require.Equal(t, TypeInventoryRefresh, pending[0].Type)
	require.Equal(t, platform.BackgroundTaskMaxRetry, pending[0].MaxRetry)
	require.Equal(t, inventoryRefreshTaskTimeout, pending[0].Timeout)
}
