package infra

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftImportTaskJSONOmitsPrivateObjectKeyAndClaimToken(t *testing.T) {
	const (
		objectKeyCanary = "imports/microsoft/source/object-key-canary.txt"
		claimToken      = "claim-token-canary"
	)
	task := coreapp.MicrosoftImportTask{
		ImportID:        42,
		OwnerUserID:     7,
		SourceObjectKey: objectKeyCanary,
		LongLived:       true,
		ErrorStrategy:   domain.ImportErrorStrategyAbort,
		RequestID:       "request-42",
		Generation:      3,
		ClaimToken:      claimToken,
	}

	payload, err := json.Marshal(task)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"importId": 42,
		"ownerUserId": 7,
		"longLived": true,
		"errorStrategy": "abort",
		"requestId": "request-42",
		"generation": 3
	}`, string(payload))
	require.NotContains(t, string(payload), objectKeyCanary)
	require.NotContains(t, string(payload), "sourceObjectKey")
	require.NotContains(t, string(payload), claimToken)
	require.NotContains(t, string(payload), "claimToken")

	var decoded coreapp.MicrosoftImportTask
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Empty(t, decoded.SourceObjectKey)
	require.Empty(t, decoded.ClaimToken)
	require.Equal(t, uint64(3), decoded.Generation)
}

func TestResourceImportQueueReportsOnlyNewGenerationAsAccepted(t *testing.T) {
	server := miniredis.RunT(t)
	redisOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	client := asynq.NewClient(redisOptions)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	queue := NewResourceImportQueue(client)
	task := coreapp.MicrosoftImportTask{
		ImportID: 42, OwnerUserID: 7, ErrorStrategy: domain.ImportErrorStrategySkip, Generation: 3,
	}

	accepted, err := queue.EnqueueMicrosoftImport(context.Background(), task)
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = queue.EnqueueMicrosoftImport(context.Background(), task)
	require.NoError(t, err)
	require.False(t, accepted)

	inspector := asynq.NewInspector(redisOptions)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })
	scheduled, err := inspector.ListScheduledTasks(importQueueName)
	require.NoError(t, err)
	require.Len(t, scheduled, 1)
	require.Equal(t, platform.BackgroundTaskMaxRetry, scheduled[0].MaxRetry)
	uniqueKeys := 0
	for _, key := range server.Keys() {
		if strings.Contains(key, ":unique:") {
			uniqueKeys++
			require.Positive(t, server.TTL(key))
		}
	}
	require.Equal(t, 1, uniqueKeys)
}
