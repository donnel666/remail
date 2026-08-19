package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestQueueAllowsTimeAndParticipantTriggersForSameLottery(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })

	queue := NewQueue(client)
	ctx := context.Background()
	require.NoError(t, queue.EnqueueDraw(ctx, 21, func() *time.Time {
		value := time.Now().Add(time.Hour)
		return &value
	}()))
	require.NoError(t, queue.EnqueueDraw(ctx, 21, nil))

	inspector := asynq.NewInspector(options)
	pending, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	scheduled, err := inspector.ListScheduledTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
}
