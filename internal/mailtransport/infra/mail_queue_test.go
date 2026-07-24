package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newMailQueueTestClient(t *testing.T) (*miniredis.Miniredis, *asynq.Client, *asynq.Inspector, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, redisClient.Close())
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	return server, client, inspector, redisClient
}

func TestInboundMailQueueDeduplicatesOneGenerationButAcceptsTheNext(t *testing.T) {
	server, client, inspector, _ := newMailQueueTestClient(t)
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

func TestOutboundMailQueueStoresFiveMinutePayloadAndQueuesOnlyReference(t *testing.T) {
	server, client, inspector, redisClient := newMailQueueTestClient(t)
	queue := NewOutboundMailQueue(client, redisClient)
	ctx := context.Background()
	message := mailapp.VerificationCodeMessage("user@example.com", "123456")

	accepted, err := queue.EnqueueOutboundSend(ctx, mailapp.OutboundSendTask{Message: message})
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueOutboundSend(ctx, mailapp.OutboundSendTask{Message: message})
	require.NoError(t, err)
	require.False(t, accepted)

	pending, err := inspector.ListPendingTasks(mailQueueName)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var payload mailapp.OutboundSendTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.NotEmpty(t, payload.ID)
	require.Empty(t, payload.Message.To)
	require.NotContains(t, string(pending[0].Payload), "123456")
	require.NotEqual(t, TypeOutboundSend, pending[0].ID)
	require.Zero(t, pending[0].MaxRetry)
	require.Equal(t, outboundPayloadTTL, server.TTL(outboundPayloadKey(payload.ID)))
	stored, found, err := queue.LoadOutboundSend(ctx, payload.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, message.To, stored.Message.To)
	require.Equal(t, message.HTMLBody, stored.Message.HTMLBody)

	server.FastForward(outboundPayloadTTL + time.Second)
	accepted, err = queue.EnqueueOutboundSend(ctx, mailapp.OutboundSendTask{Message: message})
	require.NoError(t, err)
	require.True(t, accepted)
}

func TestOutboundMailQueueRejectsSameIdempotencyKeyWithDifferentPayload(t *testing.T) {
	_, client, _, redisClient := newMailQueueTestClient(t)
	queue := NewOutboundMailQueue(client, redisClient)
	first := mailapp.VerificationCodeMessage("user@example.com", "123456")
	first.IdempotencyKey = "fixed-key"
	second := mailapp.VerificationCodeMessage("user@example.com", "654321")
	second.IdempotencyKey = "fixed-key"

	accepted, err := queue.EnqueueOutboundSend(context.Background(), mailapp.OutboundSendTask{Message: first})
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueOutboundSend(context.Background(), mailapp.OutboundSendTask{Message: second})

	require.ErrorIs(t, err, maildomain.ErrOutboundIdempotencyConflict)
	require.False(t, accepted)
}
