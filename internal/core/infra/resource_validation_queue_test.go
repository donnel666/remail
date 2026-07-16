package infra

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestResourceValidationBatchQueueUsesFencedLiveLease(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	asynqClient := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, asynqClient.Close()) })
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	queue := NewResourceValidationQueue(asynqClient, redisClient)

	task := coreapp.ResourceValidationBatchTask{BatchID: "admin-idempotency-key", OwnerUserID: 7}
	require.NoError(t, queue.EnqueueResourceValidationBatch(context.Background(), task))

	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })
	pending, err := inspector.ListPendingTasks(platform.QueueResource)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var claimed coreapp.ResourceValidationBatchTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &claimed))
	require.NotEmpty(t, claimed.ClaimToken)
	storedClaim, err := server.Get(resourceValidationBatchLeaseKey(task.BatchID))
	require.NoError(t, err)
	require.Equal(t, claimed.ClaimToken, storedClaim)

	// Replaying the HTTP idempotency key while the cursor is alive must not
	// enqueue a second first page.
	require.NoError(t, queue.EnqueueResourceValidationBatch(context.Background(), task))
	pending, err = inspector.ListPendingTasks(platform.QueueResource)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	stale := claimed
	stale.ClaimToken = "stale-token"
	owned, err := queue.RefreshResourceValidationBatch(context.Background(), stale)
	require.NoError(t, err)
	require.False(t, owned)
	require.NoError(t, queue.ReleaseResourceValidationBatch(context.Background(), stale))
	require.True(t, server.Exists(resourceValidationBatchLeaseKey(task.BatchID)))

	owned, err = queue.RefreshResourceValidationBatch(context.Background(), claimed)
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, queue.ReleaseResourceValidationBatch(context.Background(), claimed))
	require.False(t, server.Exists(resourceValidationBatchLeaseKey(task.BatchID)))

	// If an old Asynq task outlives its lease, a new submission gets a new task
	// ID and token. The old payload remains harmless because it cannot refresh
	// the replacement lease.
	require.NoError(t, queue.EnqueueResourceValidationBatch(context.Background(), task))
	pending, err = inspector.ListPendingTasks(platform.QueueResource)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	var replacement coreapp.ResourceValidationBatchTask
	for _, info := range pending {
		var candidate coreapp.ResourceValidationBatchTask
		require.NoError(t, json.Unmarshal(info.Payload, &candidate))
		if candidate.ClaimToken != claimed.ClaimToken {
			replacement = candidate
		}
	}
	require.NotEmpty(t, replacement.ClaimToken)
	owned, err = queue.RefreshResourceValidationBatch(context.Background(), claimed)
	require.NoError(t, err)
	require.False(t, owned)
	owned, err = queue.RefreshResourceValidationBatch(context.Background(), replacement)
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, queue.ReleaseResourceValidationBatch(context.Background(), replacement))
}
