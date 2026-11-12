package platform

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func fixedGateTestClients(t *testing.T) (*miniredis.Miniredis, *redis.Client, *asynq.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: server.Addr()})
	t.Cleanup(func() {
		require.NoError(t, queue.Close())
		require.NoError(t, client.Close())
	})
	return server, client, queue
}

func enqueueFixedGateTask(t *testing.T, queue *asynq.Client, name string) string {
	t.Helper()
	info, err := queue.Enqueue(asynq.NewTask("test:fixed", nil), asynq.Queue(name))
	require.NoError(t, err)
	return info.ID
}

// Model Asynq's atomic state transitions while keeping dispatcher interleavings deterministic.
func moveFixedGateTask(t *testing.T, client *redis.Client, queue, id, from, to string) {
	t.Helper()
	prefix := "asynq:{" + queue + "}:"
	ctx := context.Background()
	_, err := client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		if from == "pending" || from == "active" {
			pipe.LRem(ctx, prefix+from, 0, id)
		} else {
			pipe.ZRem(ctx, prefix+from, id)
		}
		switch to {
		case "active", "pending":
			pipe.LPush(ctx, prefix+to, id)
		case "scheduled", "retry":
			pipe.ZAdd(ctx, prefix+to, redis.Z{Score: float64(time.Now().Add(time.Hour).Unix()), Member: id})
		}
		pipe.HSet(ctx, prefix+"t:"+id, "state", to)
		return nil
	})
	require.NoError(t, err)
}

func TestFixedBackgroundGateCountsAllOutstandingStatesAcrossRestart(t *testing.T) {
	_, client, queue := fixedGateTestClients(t)
	ctx := context.Background()
	for i, state := range []string{"pending", "active", "scheduled", "retry"} {
		name := []string{"personal", "enterprise"}[i%2]
		id := enqueueFixedGateTask(t, queue, name)
		moveFixedGateTask(t, client, name, id, "pending", state)
	}
	gate := NewFixedBackgroundExecutionGate(5, client, "validation", "personal", "enterprise")
	require.NoError(t, gate.Dispatch(ctx, 10, func(ctx context.Context, limit int) error {
		require.Equal(t, 1, limit)
		_, err := queue.EnqueueContext(ctx, asynq.NewTask("test:fixed", nil), asynq.Queue("personal"))
		return err
	}))
	restarted := NewFixedBackgroundExecutionGate(5, client, "validation", "personal", "enterprise")
	require.NoError(t, restarted.Dispatch(ctx, 10, func(context.Context, int) error {
		t.Fatal("all five durable tasks must still occupy the pool after restart")
		return nil
	}))
}

func TestFixedBackgroundGateOldRetriesCannotSpendDispatchBudget(t *testing.T) {
	_, client, queue := fixedGateTestClients(t)
	ctx := context.Background()
	gate := NewFixedBackgroundExecutionGate(4, client, "validation", "validation")
	other := NewFixedBackgroundExecutionGate(4, client, "validation", "validation")
	var retries []string
	for range 2 {
		id := enqueueFixedGateTask(t, queue, "validation")
		moveFixedGateTask(t, client, "validation", id, "pending", "retry")
		retries = append(retries, id)
	}
	require.NoError(t, gate.Dispatch(ctx, 4, func(ctx context.Context, limit int) error {
		require.Equal(t, 2, limit)
		for _, id := range retries {
			moveFixedGateTask(t, client, "validation", id, "retry", "active")
			release, admitted, err := other.acquireTask(ctx, "validation", id)
			require.NoError(t, err)
			require.True(t, admitted)
			release()
			moveFixedGateTask(t, client, "validation", id, "active", "completed")
		}
		require.NoError(t, other.Dispatch(ctx, 4, func(context.Context, int) error {
			t.Fatal("another dispatcher must not enter between selection and enqueue")
			return nil
		}))
		for range limit {
			enqueueFixedGateTask(t, queue, "validation")
		}
		return nil
	}))
	require.NoError(t, other.Dispatch(ctx, 4, func(_ context.Context, limit int) error {
		require.Equal(t, 2, limit)
		for range limit {
			enqueueFixedGateTask(t, queue, "validation")
		}
		return nil
	}))
	require.EqualValues(t, 4, client.LLen(ctx, "asynq:{validation}:pending").Val())
	require.NoError(t, gate.Dispatch(ctx, 4, func(context.Context, int) error {
		t.Fatal("the pool is full")
		return nil
	}))
}

func TestFixedBackgroundGatePartialDispatchKeepsOnlyDurableTasks(t *testing.T) {
	_, client, queue := fixedGateTestClients(t)
	ctx := context.Background()
	gate := NewFixedBackgroundExecutionGate(2, client, "alias", "alias")
	failure := errors.New("database activation failed after enqueue")
	require.ErrorIs(t, gate.Dispatch(ctx, 2, func(context.Context, int) error {
		enqueueFixedGateTask(t, queue, "alias")
		return failure
	}), failure)
	require.NoError(t, gate.Dispatch(ctx, 2, func(_ context.Context, limit int) error {
		require.Equal(t, 1, limit, "a failed activation must not free an already enqueued task")
		return nil
	}))
}

func TestFixedBackgroundGateExecutionOwnershipAndRecovery(t *testing.T) {
	_, client, queue := fixedGateTestClients(t)
	ctx := context.Background()
	first := NewFixedBackgroundExecutionGate(2, client, "alias", "alias")
	second := NewFixedBackgroundExecutionGate(2, client, "alias", "alias")
	var ids []string
	for range 3 {
		id := enqueueFixedGateTask(t, queue, "alias")
		moveFixedGateTask(t, client, "alias", id, "pending", "active")
		ids = append(ids, id)
	}
	releaseOld, admitted, err := first.acquireTask(ctx, "alias", ids[0])
	require.NoError(t, err)
	require.True(t, admitted)
	_, admitted, err = second.acquireTask(ctx, "alias", ids[1])
	require.NoError(t, err)
	require.True(t, admitted)
	_, admitted, err = first.acquireTask(ctx, "alias", ids[2])
	require.NoError(t, err)
	require.False(t, admitted, "an old backlog cannot exceed the execution cap")

	moveFixedGateTask(t, client, "alias", ids[0], "active", "retry")
	moveFixedGateTask(t, client, "alias", ids[0], "retry", "active")
	releaseNew, admitted, err := second.acquireTask(ctx, "alias", ids[0])
	require.NoError(t, err)
	require.True(t, admitted, "a retried task can replace its abandoned execution owner")
	releaseOld()
	require.EqualValues(t, 2, client.HLen(ctx, first.activeKey).Val(), "old cleanup must not free a replacement owner")
	releaseNew()
	releaseNew()
	require.EqualValues(t, 1, client.HLen(ctx, first.activeKey).Val())

	moveFixedGateTask(t, client, "alias", ids[1], "active", "retry")
	_, admitted, err = first.acquireTask(ctx, "alias", ids[2])
	require.NoError(t, err)
	require.True(t, admitted, "Asynq recovery must also recover an abandoned permit")
	require.EqualValues(t, 1, client.HLen(ctx, first.activeKey).Val())
}

func TestFixedBackgroundGateDispatchLockExpiresAndReleaseIsFenced(t *testing.T) {
	server, client, _ := fixedGateTestClients(t)
	ctx := context.Background()
	gate := NewFixedBackgroundExecutionGate(2, client, "alias", "alias")
	require.NoError(t, client.Set(ctx, gate.dispatchKey, "crashed-dispatcher", fixedBackgroundDispatchLockTTL).Err())
	require.NoError(t, gate.Dispatch(ctx, 2, func(context.Context, int) error {
		t.Fatal("a live dispatch lock must be respected")
		return nil
	}))
	server.FastForward(fixedBackgroundDispatchLockTTL)
	require.NoError(t, gate.Dispatch(ctx, 2, func(ctx context.Context, limit int) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), fixedBackgroundDispatchTimeout)
		require.Equal(t, 2, limit)
		require.NoError(t, client.Set(ctx, gate.dispatchKey, "replacement-dispatcher", fixedBackgroundDispatchLockTTL).Err())
		return nil
	}))
	require.Equal(t, "replacement-dispatcher", client.Get(ctx, gate.dispatchKey).Val())
}

func TestFixedBackgroundGateFailsClosedOnRedisErrors(t *testing.T) {
	server, client, _ := fixedGateTestClients(t)
	gate := NewFixedBackgroundExecutionGate(2, client, "alias", "alias")
	server.SetError("unavailable")
	require.Error(t, gate.Dispatch(context.Background(), 2, func(context.Context, int) error {
		t.Fatal("Redis failure must not be treated as unused capacity")
		return nil
	}))
	_, admitted, err := gate.acquireTask(context.Background(), "alias", "task")
	require.Error(t, err)
	require.False(t, admitted)
}

func TestFixedBackgroundGateUsesRealWorkerIdentity(t *testing.T) {
	server, client, queue := fixedGateTestClients(t)
	gate := NewFixedBackgroundExecutionGate(2, client, "alias", "alias")
	for range 3 {
		enqueueFixedGateTask(t, queue, "alias")
	}
	type admission struct {
		admitted bool
		err      error
	}
	results := make(chan admission, 3)
	finish := make(chan struct{})
	defer close(finish)
	workers := asynq.NewServer(asynq.RedisClientOpt{Addr: server.Addr()}, asynq.Config{
		Concurrency: 3, Queues: map[string]int{"alias": 1}, TaskCheckInterval: 10 * time.Millisecond,
		ShutdownTimeout: time.Second,
	})
	require.NoError(t, workers.Start(asynq.HandlerFunc(func(ctx context.Context, _ *asynq.Task) error {
		release, admitted, err := gate.TryAcquireContext(ctx)
		results <- admission{admitted, err}
		if err != nil {
			return err
		}
		if !admitted {
			return ErrBackgroundExecutionDeferred
		}
		defer release()
		<-finish
		return nil
	})))
	t.Cleanup(workers.Shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var admitted int
	for range 3 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			if result.admitted {
				admitted++
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	require.Equal(t, 2, admitted)
}
