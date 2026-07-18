package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestCandidateRefreshQueueReportsOnlyNewlyAcceptedTask(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	queue := NewCandidateRefreshQueue(client)
	task := allocapp.CandidateRefreshTask{ProjectID: 10, Generation: 7, RequestID: "request-7"}

	accepted, err := queue.EnqueueCandidateRefresh(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueCandidateRefresh(context.Background(), task)
	require.NoError(t, err)
	require.False(t, accepted)

	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })
	pending, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NotEqual(t, TypeCandidateRefresh+":10", pending[0].ID)
	require.Equal(t, platform.BackgroundTaskMaxRetry, pending[0].MaxRetry)
	require.Equal(t, candidateRefreshTaskTimeout, pending[0].Timeout)
	require.Zero(t, pending[0].Retention)
	var payload allocapp.CandidateRefreshTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, task, payload)

	server.FastForward(candidateRefreshTaskTimeout + time.Second)
	accepted, err = queue.EnqueueCandidateRefresh(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted, "the duplicate-suppression key must have a finite TTL")
}
