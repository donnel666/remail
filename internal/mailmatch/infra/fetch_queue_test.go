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
