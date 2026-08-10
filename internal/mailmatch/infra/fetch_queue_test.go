package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestPickupRequestQueueSnapshotsExpiration(t *testing.T) {
	defer runtimeconfig.Delete("pickup_request_fetch_timeout_minutes")
	runtimeconfig.Set("pickup_request_fetch_timeout_minutes", "3")
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	requestedAt := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	accepted, err := NewFetchQueue(client).EnqueuePickupRequest(context.Background(), mailmatchapp.PickupRequestFetchTask{
		RequestedAt: requestedAt,
		Scopes:      []mailmatchapp.PickupRequestFetchScope{{EmailResourceID: 1, OrderNo: "ORDER-1"}},
	})

	require.NoError(t, err)
	require.True(t, accepted)
	pending, err := inspector.ListPendingTasks(platform.QueueMailfetch)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var payload mailmatchapp.PickupRequestFetchTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, requestedAt.Add(3*time.Minute), payload.ExpiresAt)

	runtimeconfig.Set("pickup_request_fetch_timeout_minutes", "1")
	require.Equal(t, requestedAt.Add(3*time.Minute), payload.EffectiveExpiresAt())
}

func TestValidatedHistoryQueueRetriesThreeTimes(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})

	require.NoError(t, NewFetchQueue(client).EnqueueValidatedMicrosoftHistoryScan(
		context.Background(),
		mailmatchapp.ValidatedMicrosoftHistoryScanTask{ResourceID: 1},
	))
	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundProjectHistory)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, 3, ValidatedHistoryTaskMaxRetry)
	require.Equal(t, ValidatedHistoryTaskMaxRetry, pending[0].MaxRetry)
}

func TestAdminResourceFetchQueueUsesDefault(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	queue := NewFetchQueue(client)

	accepted, err := queue.EnqueueAdminResourceFetch(context.Background(), mailmatchapp.AdminResourceFetchTask{
		ResourceID: 101, Generation: 1, RequestID: "admin-fetch",
	})
	require.NoError(t, err)
	require.True(t, accepted)

	foreground, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, foreground, 1)
	require.Equal(t, TypeMailmatchAdminResourceFetch, foreground[0].Type)
	var fetchPayload mailmatchapp.AdminResourceFetchTask
	require.NoError(t, json.Unmarshal(foreground[0].Payload, &fetchPayload))
	require.Equal(t, uint(101), fetchPayload.ResourceID)
}

func TestResourceHistoryQueueUsesBackgroundProjectHistory(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})

	accepted, err := NewFetchQueue(client).EnqueueResourceHistory(context.Background(), mailmatchapp.ResourceHistoryTask{
		ResourceID: 102, Generation: 1, RequestID: "history",
	})
	require.NoError(t, err)
	require.True(t, accepted)

	history, err := inspector.ListPendingTasks(platform.QueueBackgroundProjectHistory)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, TypeMailmatchResourceHistory, history[0].Type)
	var historyPayload mailmatchapp.ResourceHistoryTask
	require.NoError(t, json.Unmarshal(history[0].Payload, &historyPayload))
	require.Equal(t, uint(102), historyPayload.ResourceID)
}

func TestResourceFetchDispatcherUsesForegroundQueue(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})

	_, err := NewFetchQueue(client).EnqueueAdminResourceFetch(context.Background(), mailmatchapp.AdminResourceFetchTask{
		ResourceID: 103, Generation: 1,
	})
	require.NoError(t, err)
	require.NoError(t, NewFetchQueue(client).EnqueueAdminResourceFetchDispatcher(context.Background(), time.Second))
	pending, err := inspector.ListPendingTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	scheduled, err := inspector.ListScheduledTasks(platform.QueueDefault)
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
	require.Equal(t, TypeMailmatchAdminResourceFetchDispatcher, scheduled[0].Type)
}

func TestResourceHistoryDispatcherUsesBackgroundProjectHistoryQueue(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})

	require.NoError(t, NewFetchQueue(client).EnqueueResourceHistoryDispatcher(context.Background(), time.Second))
	scheduled, err := inspector.ListScheduledTasks(platform.QueueBackgroundProjectHistory)
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
	require.Equal(t, TypeMailmatchResourceHistoryDispatcher, scheduled[0].Type)

	queues, err := inspector.Queues()
	require.NoError(t, err)
	require.Contains(t, queues, platform.QueueBackgroundProjectHistory)
	require.NotContains(t, queues, platform.QueueDefault)
}
