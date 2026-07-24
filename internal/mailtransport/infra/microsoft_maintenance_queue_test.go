package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftMaintenanceTaskTimeoutsCoverRuntimeBudgets(t *testing.T) {
	runtimeconfig.Set("max_proxy_attempts", "3")
	runtimeconfig.Set("oauth_validation_timeout_seconds", "60")
	runtimeconfig.Set("password_recovery_code_wait_seconds", "1800")
	t.Cleanup(func() {
		runtimeconfig.Delete("max_proxy_attempts")
		runtimeconfig.Delete("oauth_validation_timeout_seconds")
		runtimeconfig.Delete("password_recovery_code_wait_seconds")
	})

	require.Equal(t, 4*time.Minute, microsoftTokenRefreshTimeout())
	require.Equal(t, 48*time.Minute+30*time.Second, microsoftAliasTimeout())
}

func TestMicrosoftTokenRefreshQueueReportsOnlyNewAcceptance(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	queue := NewMicrosoftTokenRefreshQueue(client)
	task := mailapp.MicrosoftTokenRefreshTask{
		ResourceID: 42, Generation: 7, ExpectedCredentialRevision: 3, RequestID: "request-7",
	}

	accepted, err := queue.EnqueueMicrosoftTokenRefresh(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueMicrosoftTokenRefresh(context.Background(), task)
	require.NoError(t, err)
	require.False(t, accepted)

	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundTokenRefresh)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NotEqual(t, TypeMicrosoftTokenRefresh+":42", pending[0].ID)
	require.Equal(t, microsoftTokenRefreshTaskTimeout, pending[0].Timeout)
	require.Zero(t, pending[0].Retention)
	var payload mailapp.MicrosoftTokenRefreshTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, task, payload)

	server.FastForward(microsoftTokenRefreshTaskTimeout + time.Second)
	accepted, err = queue.EnqueueMicrosoftTokenRefresh(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted)
}

func TestMicrosoftAliasQueueReportsOnlyNewAcceptance(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, client.Close())
	})
	queue := NewMicrosoftAliasQueue(client)
	task := mailapp.MicrosoftAliasTask{ResourceID: 42, Generation: 7}

	accepted, err := queue.EnqueueMicrosoftAlias(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueMicrosoftAlias(context.Background(), task)
	require.NoError(t, err)
	require.False(t, accepted)

	pending, err := inspector.ListPendingTasks(platform.QueueBackgroundAlias)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NotEqual(t, TypeMicrosoftAlias+":42", pending[0].ID)
	require.Equal(t, microsoftAliasTaskTimeout, pending[0].Timeout)
	require.Zero(t, pending[0].Retention)
	var payload mailapp.MicrosoftAliasTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, task, payload)

	server.FastForward(microsoftAliasTaskTimeout + time.Second)
	accepted, err = queue.EnqueueMicrosoftAlias(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted)
}
