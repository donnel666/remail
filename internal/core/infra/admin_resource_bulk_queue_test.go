package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAdminResourceBulkQueueFencesIdempotencyAndCursor(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	asynqClient := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, asynqClient.Close()) })
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	queue := NewAdminResourceBulkQueue(asynqClient, redisClient)
	task := coreapp.AdminResourceBulkTask{
		BatchID: "operator-idempotency", RequestFingerprint: "fingerprint-a", CommandID: 42,
		Action: coreapp.AdminResourceBulkPublish, OperatorUserID: 9,
		Selection: coreapp.AdminResourceBulkSelection{Mode: coreapp.AdminResourceBulkIDs, ResourceIDs: []uint{1, 2}},
	}

	accepted, err := queue.EnqueueAdminResourceBulk(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueAdminResourceBulk(context.Background(), task)
	require.NoError(t, err)
	require.False(t, accepted)
	conflict := task
	conflict.RequestFingerprint = "fingerprint-b"
	_, err = queue.EnqueueAdminResourceBulk(context.Background(), conflict)
	require.ErrorIs(t, err, domain.ErrResourceIdempotencyConflict)

	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })
	pending, err := inspector.ListPendingTasks(AdminResourceBulkQueueName)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var queued coreapp.AdminResourceBulkTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &queued))
	require.NotEmpty(t, queued.ClaimToken)
	require.Positive(t, server.TTL(adminResourceBulkLeaseKey(task.BatchID)))
	tasks := governanceapp.NewAdminTaskQueryService(governanceinfra.NewAdminTaskViewRepo(nil, redisClient))
	view, err := tasks.Get(context.Background(), fmt.Sprintf("bulk:%d", task.CommandID))
	require.NoError(t, err)
	require.Equal(t, governanceapp.AdminTaskStatusQueued, view.Status)
	require.EqualValues(t, 2, view.Progress.Total)

	owned, err := queue.RefreshAdminResourceBulk(context.Background(), queued)
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, queue.RecordAdminResourceBulkPage(context.Background(), queued, coreapp.AdminResourceBulkPageResult{
		Affected: 1, AfterID: 1, ThroughID: 2,
	}))
	// Retrying a page after its business transaction committed must not double-count progress.
	require.NoError(t, queue.RecordAdminResourceBulkPage(context.Background(), queued, coreapp.AdminResourceBulkPageResult{
		Affected: 1, AfterID: 1, ThroughID: 2,
	}))

	next := queued
	next.AfterID = 1
	next.ThroughID = 2
	owned, err = queue.RefreshAdminResourceBulk(context.Background(), next)
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, queue.RecordAdminResourceBulkPage(context.Background(), next, coreapp.AdminResourceBulkPageResult{
		Skipped: 1, AfterID: 2, ThroughID: 2, Done: true,
		ReasonCounts: map[string]int64{"already_target": 1},
	}))

	view, err = tasks.Get(context.Background(), fmt.Sprintf("bulk:%d", task.CommandID))
	require.NoError(t, err)
	require.Equal(t, governanceapp.AdminTaskStatusSucceeded, view.Status)
	require.EqualValues(t, 2, view.Progress.Processed)
	require.EqualValues(t, 1, view.Progress.Succeeded)
	require.EqualValues(t, 1, view.Progress.Skipped)
	require.Equal(t, []governanceapp.AdminTaskReasonCount{{Reason: "already_target", Count: 1}}, view.Progress.ReasonCounts)

	require.NoError(t, queue.ReleaseAdminResourceBulk(context.Background(), queued))
	require.False(t, server.Exists(adminResourceBulkLeaseKey(task.BatchID)))
	require.True(t, server.Exists(governanceinfra.AdminResourceBulkStatusKey(task.CommandID)))
}
