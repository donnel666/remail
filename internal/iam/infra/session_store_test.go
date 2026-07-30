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

func TestSessionStoreConsumesOAuthFlowOnce(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := NewSessionStore(client)
	flow := app.OAuthFlow{
		Provider:     "linuxdo",
		Intent:       "bind",
		UserID:       7,
		SessionID:    "session-a",
		CodeVerifier: "verifier",
	}

	require.NoError(t, store.PutOAuthFlow(context.Background(), "state", flow, time.Minute))
	require.True(t, server.Exists(linuxDOFlowKey("state")))
	require.False(t, server.Exists(oauthFlowKey("state")))
	consumed, err := store.ConsumeOAuthFlow(context.Background(), "state")
	require.NoError(t, err)
	require.Equal(t, &flow, consumed)

	consumed, err = store.ConsumeOAuthFlow(context.Background(), "state")
	require.NoError(t, err)
	require.Nil(t, consumed)
}

func TestSessionStoreConsumesLegacyLinuxDOFlowWithoutProvider(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := NewSessionStore(client)
	require.NoError(t, server.Set(linuxDOFlowKey("legacy-state"), `{"Intent":"login","CodeVerifier":"verifier"}`))

	flow, err := store.ConsumeOAuthFlow(context.Background(), "legacy-state")

	require.NoError(t, err)
	require.Equal(t, "linuxdo", flow.Provider)
	require.Equal(t, "login", flow.Intent)
	require.Equal(t, "verifier", flow.CodeVerifier)
	require.False(t, server.Exists(linuxDOFlowKey("legacy-state")))
}

func TestSessionStoreKeepsGitHubFlowSeparateFromLegacyLinuxDO(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := NewSessionStore(client)
	flow := app.OAuthFlow{Provider: "github", Intent: "login"}

	require.NoError(t, store.PutOAuthFlow(context.Background(), "github-state", flow, time.Minute))
	require.True(t, server.Exists(oauthFlowKey("github-state")))
	require.False(t, server.Exists(linuxDOFlowKey("github-state")))
	consumed, err := store.ConsumeOAuthFlow(context.Background(), "github-state")
	require.NoError(t, err)
	require.Equal(t, &flow, consumed)
}

func TestSessionStoreKeepsLinuxDOPendingServerSideUntilCompleted(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := NewSessionStore(client)
	pending := app.LinuxDOPending{
		Profile:        app.LinuxDOProfile{ID: "42", Username: "linuxdo-user", Email: "owner@qq.com", Active: true},
		SuggestedEmail: "owner@qq.com",
	}

	require.NoError(t, store.PutLinuxDOPending(context.Background(), "opaque-token", pending, time.Minute))
	stored, err := store.GetLinuxDOPending(context.Background(), "opaque-token")
	require.NoError(t, err)
	require.Equal(t, &pending, stored)
	require.NoError(t, store.DeleteLinuxDOPending(context.Background(), "opaque-token"))
	stored, err = store.GetLinuxDOPending(context.Background(), "opaque-token")
	require.NoError(t, err)
	require.Nil(t, stored)
}

func TestSessionStoreKeepsGitHubPendingServerSideUntilCompleted(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := NewSessionStore(client)
	pending := app.GitHubPending{
		Profile: app.GitHubProfile{ID: "42", Username: "octocat", Email: "owner@example.com", CreatedAt: time.Now().AddDate(-2, 0, 0)},
		Intent:  "login",
		UserID:  7,
		Email:   "owner@example.com",
	}

	require.NoError(t, store.PutGitHubPending(context.Background(), "opaque-token", pending, time.Minute))
	stored, err := store.GetGitHubPending(context.Background(), "opaque-token")
	require.NoError(t, err)
	require.Equal(t, &pending, stored)
	require.NoError(t, store.DeleteGitHubPending(context.Background(), "opaque-token"))
	stored, err = store.GetGitHubPending(context.Background(), "opaque-token")
	require.NoError(t, err)
	require.Nil(t, stored)
}
