package infra

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestRechargeQueueUsesRealtimePaymentQueue(t *testing.T) {
	server := miniredis.RunT(t)
	options := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(options)
	inspector := asynq.NewInspector(options)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	queue := NewRechargeQueue(client)
	task := billingapp.RechargeTask{RechargeNo: "RC1"}

	require.NoError(t, queue.Enqueue(context.Background(), task))
	require.NoError(t, queue.Enqueue(context.Background(), task))
	pending, err := inspector.ListPendingTasks(platform.QueuePaymentReconcile)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, TypeRechargeReconcile, pending[0].Type)
	require.Equal(t, 5, pending[0].MaxRetry)
	require.Equal(t, domain.RechargeQueryLease, pending[0].Timeout)
	require.Zero(t, pending[0].Retention)
	var payload billingapp.RechargeTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, task, payload)
}
