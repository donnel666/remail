package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/donnel666/remail/internal/iam/app"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSessionStoreConsumesLinuxDOFlowOnce(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := NewSessionStore(client)
	flow := app.LinuxDOFlow{
		Intent:       "bind",
		UserID:       7,
		SessionID:    "session-a",
		CodeVerifier: "verifier",
	}

	require.NoError(t, store.PutLinuxDOFlow(context.Background(), "state", flow, time.Minute))
	consumed, err := store.ConsumeLinuxDOFlow(context.Background(), "state")
	require.NoError(t, err)
	require.Equal(t, &flow, consumed)

	consumed, err = store.ConsumeLinuxDOFlow(context.Background(), "state")
	require.NoError(t, err)
	require.Nil(t, consumed)
}
