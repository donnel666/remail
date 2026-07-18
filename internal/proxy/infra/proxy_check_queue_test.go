package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func newProxyQueueTestClient(t *testing.T) (*miniredis.Miniredis, asynq.RedisClientOpt, *asynq.Client, *asynq.Inspector, *ProxyCheckQueue) {
	t.Helper()
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	return server, redisOptions, client, inspector, NewProxyCheckQueue(client)
}

func TestProxyCheckQueueDeduplicatesOneGenerationButAcceptsTheNext(t *testing.T) {
	_, _, _, inspector, queue := newProxyQueueTestClient(t)
	ctx := context.Background()

	accepted, err := queue.EnqueueProxyCheck(ctx, proxyapp.ProxyCheckTask{ProxyID: 42, CheckGeneration: 7})
	require.NoError(t, err)
	require.True(t, accepted)

	accepted, err = queue.EnqueueProxyCheck(ctx, proxyapp.ProxyCheckTask{ProxyID: 42, CheckGeneration: 7})
	require.NoError(t, err)
	require.False(t, accepted)

	accepted, err = queue.EnqueueProxyCheck(ctx, proxyapp.ProxyCheckTask{ProxyID: 42, CheckGeneration: 8})
	require.NoError(t, err)
	require.True(t, accepted)

	pending, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	generations := map[uint64]bool{}
	for _, info := range pending {
		var payload proxyapp.ProxyCheckTask
		require.NoError(t, json.Unmarshal(info.Payload, &payload))
		require.Equal(t, uint(42), payload.ProxyID)
		generations[payload.CheckGeneration] = true
		require.NotEqual(t, TypeProxyCheck, info.ID)
		require.Equal(t, platform.BackgroundTaskMaxRetry, info.MaxRetry)
	}
	require.Equal(t, map[uint64]bool{7: true, 8: true}, generations)
}

func TestArchivedLegacyProxyDispatcherCannotBlockRandomDispatcher(t *testing.T) {
	server, _, client, inspector, queue := newProxyQueueTestClient(t)
	ctx := context.Background()

	legacy, err := client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeProxyCheckDispatcher, nil),
		asynq.Queue(platform.QueueDefault),
		asynq.TaskID(TypeProxyCheckDispatcher),
		asynq.MaxRetry(0),
	)
	require.NoError(t, err)
	require.NoError(t, inspector.ArchiveTask(platform.QueueDefault, legacy.ID))

	require.NoError(t, queue.EnqueueProxyCheckDispatcher(ctx, 0))
	pending, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	firstID := pending[0].ID
	require.NotEqual(t, TypeProxyCheckDispatcher, firstID)

	require.NoError(t, queue.EnqueueProxyCheckDispatcher(ctx, 0))
	pending, err = inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	server.FastForward(proxyCheckDispatcherTaskTimeout + time.Second)
	require.NoError(t, queue.EnqueueProxyCheckDispatcher(ctx, 0))
	pending, err = inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	require.NotEqual(t, firstID, pending[1].ID)
}
