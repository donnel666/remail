package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func newMailQueueTestClient(t *testing.T) (*miniredis.Miniredis, *asynq.Client, *asynq.Inspector) {
	t.Helper()
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	return server, client, inspector
}

func TestInboundMailQueueDeduplicatesOneGenerationButAcceptsTheNext(t *testing.T) {
	server, client, inspector := newMailQueueTestClient(t)
	queue := NewInboundMailQueue(client)
	ctx := context.Background()

	accepted, err := queue.EnqueueInboundProcess(ctx, mailapp.InboundProcessTask{InboundMailID: 42, ProcessGeneration: 7})
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueInboundProcess(ctx, mailapp.InboundProcessTask{InboundMailID: 42, ProcessGeneration: 7})
	require.NoError(t, err)
	require.False(t, accepted)
	accepted, err = queue.EnqueueInboundProcess(ctx, mailapp.InboundProcessTask{InboundMailID: 42, ProcessGeneration: 8})
	require.NoError(t, err)
	require.True(t, accepted)

	pending, err := inspector.ListPendingTasks(mailQueueName)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	generations := map[uint64]bool{}
	for _, info := range pending {
		var payload mailapp.InboundProcessTask
		require.NoError(t, json.Unmarshal(info.Payload, &payload))
		require.Equal(t, uint(42), payload.InboundMailID)
		generations[payload.ProcessGeneration] = true
		require.NotEqual(t, TypeInboundProcess, info.ID)
		require.Equal(t, platform.BackgroundTaskMaxRetry, info.MaxRetry)
	}
	require.Equal(t, map[uint64]bool{7: true, 8: true}, generations)

	server.FastForward(inboundTaskTimeout + time.Second)
	accepted, err = queue.EnqueueInboundProcess(ctx, mailapp.InboundProcessTask{InboundMailID: 42, ProcessGeneration: 7})
	require.NoError(t, err)
	require.True(t, accepted)
}

func TestOutboundMailQueueDeduplicatesOneGenerationButAcceptsTheNext(t *testing.T) {
	server, client, inspector := newMailQueueTestClient(t)
	queue := NewOutboundMailQueue(client)
	ctx := context.Background()

	accepted, err := queue.EnqueueOutboundSend(ctx, mailapp.OutboundSendTask{IdempotencyKey: "mail-42", SendGeneration: 7})
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueOutboundSend(ctx, mailapp.OutboundSendTask{IdempotencyKey: "mail-42", SendGeneration: 7})
	require.NoError(t, err)
	require.False(t, accepted)
	accepted, err = queue.EnqueueOutboundSend(ctx, mailapp.OutboundSendTask{IdempotencyKey: "mail-42", SendGeneration: 8})
	require.NoError(t, err)
	require.True(t, accepted)

	pending, err := inspector.ListPendingTasks(mailQueueName)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	generations := map[uint64]bool{}
	for _, info := range pending {
		var payload mailapp.OutboundSendTask
		require.NoError(t, json.Unmarshal(info.Payload, &payload))
		require.Equal(t, "mail-42", payload.IdempotencyKey)
		generations[payload.SendGeneration] = true
		require.NotEqual(t, TypeOutboundSend, info.ID)
		require.Equal(t, platform.BackgroundTaskMaxRetry, info.MaxRetry)
	}
	require.Equal(t, map[uint64]bool{7: true, 8: true}, generations)

	server.FastForward(outboundTaskTimeout + time.Second)
	accepted, err = queue.EnqueueOutboundSend(ctx, mailapp.OutboundSendTask{IdempotencyKey: "mail-42", SendGeneration: 7})
	require.NoError(t, err)
	require.True(t, accepted)
}
