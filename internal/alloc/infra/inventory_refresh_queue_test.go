package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

func TestPrivateInventoryRefreshQueueDeduplicatesPerProjectAndViewer(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	queue := NewInventoryRefreshQueue(client)
	ctx := context.Background()
	for range 5 {
		require.NoError(t, queue.EnqueuePrivateInventoryRefresh(ctx, 10, 7))
	}
	require.NoError(t, queue.EnqueuePrivateInventoryRefresh(ctx, 10, 8))
	require.NoError(t, queue.EnqueuePrivateInventoryRefresh(ctx, 11, 7))
	require.Error(t, queue.EnqueuePrivateInventoryRefresh(ctx, 0, 7))
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() { _ = inspector.Close() })
	tasks, err := inspector.ListPendingTasks(platform.QueueBackgroundInventory)
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	seen := map[PrivateInventoryRefreshPayload]bool{}
	for _, task := range tasks {
		require.Equal(t, TypePrivateInventoryRefresh, task.Type)
		require.Equal(t, 2*time.Minute, task.Timeout)
		require.Equal(t, platform.BackgroundTaskMaxRetry, task.MaxRetry)
		var payload PrivateInventoryRefreshPayload
		require.NoError(t, json.Unmarshal(task.Payload, &payload))
		seen[payload] = true
	}
	require.Len(t, seen, 3)
}
