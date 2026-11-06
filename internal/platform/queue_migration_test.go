package platform

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestMigrateLegacyDashboardTasksPreservesPendingAndScheduledWork(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})

	pending, err := client.Enqueue(asynq.NewTask("dashboard:ranking_refresh", []byte(`{"v":1}`)), asynq.Queue(QueueBackgroundInventory))
	require.NoError(t, err)
	scheduled, err := client.Enqueue(asynq.NewTask("dashboard:admin_refresh", nil), asynq.Queue(QueueBackgroundInventory), asynq.ProcessIn(time.Hour))
	require.NoError(t, err)
	_, err = client.Enqueue(asynq.NewTask("inventory:refresh", nil), asynq.Queue(QueueBackgroundInventory))
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, migrateLegacyDashboardTasks(context.Background(), client, rdb))

	oldPending, err := inspector.ListPendingTasks(QueueBackgroundInventory)
	require.NoError(t, err)
	require.Len(t, oldPending, 1)
	require.Equal(t, "inventory:refresh", oldPending[0].Type)
	oldScheduled, err := inspector.ListScheduledTasks(QueueBackgroundInventory)
	require.NoError(t, err)
	require.Empty(t, oldScheduled)

	newPending, err := inspector.ListPendingTasks(QueueBackgroundDashboard)
	require.NoError(t, err)
	require.Len(t, newPending, 1)
	require.Equal(t, pending.ID, newPending[0].ID)
	require.Equal(t, []byte(`{"v":1}`), newPending[0].Payload)
	_, err = client.Enqueue(asynq.NewTask("dashboard:ranking_refresh", []byte(`{"v":1}`)), asynq.Queue(QueueBackgroundDashboard), asynq.Unique(dashboardRankingMigrationUniqueTTL))
	require.ErrorIs(t, err, asynq.ErrDuplicateTask)
	newScheduled, err := inspector.ListScheduledTasks(QueueBackgroundDashboard)
	require.NoError(t, err)
	require.Len(t, newScheduled, 1)
	require.Equal(t, scheduled.ID, newScheduled[0].ID)
	require.Greater(t, newScheduled[0].NextProcessAt, time.Now())
}
