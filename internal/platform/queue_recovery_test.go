package platform

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestLiveTaskResourceIDsIncludesEveryRunnableState(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx := context.Background()
	const queue = "recovery_test"
	prefix := "asynq:{" + queue + "}:"
	for index, state := range []string{"pending", "active", "scheduled", "retry", "archived"} {
		resourceID := uint(index + 1)
		payload, err := json.Marshal(struct {
			ResourceID uint `json:"resourceId"`
		}{ResourceID: resourceID})
		require.NoError(t, err)
		key := prefix + "t:" + string(rune('a'+index))
		require.NoError(t, client.HSet(ctx, key,
			"msg", recoveryTaskMessage("work", payload),
			"state", state,
		).Err())
	}

	live, err := LiveTaskResourceIDs(ctx, client, queue, "work", map[uint]struct{}{1: {}, 2: {}, 3: {}, 4: {}, 5: {}})

	require.NoError(t, err)
	require.Equal(t, map[uint]struct{}{1: {}, 2: {}, 3: {}, 4: {}}, live)
}

func TestLiveTaskResourceIDsFindsCandidateBeyondThousandTasks(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx := context.Background()
	const queue = "large_recovery_test"
	prefix := "asynq:{" + queue + "}:t:"
	for index := 0; index < 1100; index++ {
		payload, err := json.Marshal(struct {
			ResourceID uint `json:"resourceId"`
		}{ResourceID: uint(index + 1)})
		require.NoError(t, err)
		require.NoError(t, client.HSet(ctx, prefix+string(rune(index%26))+string(rune(index/26)),
			"msg", recoveryTaskMessage("work", payload), "state", "pending").Err())
	}

	live, err := LiveTaskResourceIDs(ctx, client, queue, "work", map[uint]struct{}{1100: {}})

	require.NoError(t, err)
	require.Equal(t, map[uint]struct{}{1100: {}}, live)
}

func TestLiveTaskResourceIDsHonorsCancellation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	live, err := LiveTaskResourceIDs(ctx, client, "busy", "work", map[uint]struct{}{1: {}})

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, live)
}

func recoveryTaskMessage(taskType string, payload []byte) []byte {
	result := []byte{0x0a, byte(len(taskType))}
	result = append(result, taskType...)
	result = append(result, 0x12, byte(len(payload)))
	return append(result, payload...)
}
