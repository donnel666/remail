package infra

import (
	"encoding/json"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftImportTaskJSONOmitsPrivateObjectKeyAndClaimToken(t *testing.T) {
	const (
		objectKeyCanary = "imports/microsoft/source/object-key-canary.txt"
		claimToken      = "claim-token-canary"
		dispatchToken   = "opaque-dispatch-token"
	)
	task := coreapp.MicrosoftImportTask{
		ImportID:        42,
		OwnerUserID:     7,
		SourceObjectKey: objectKeyCanary,
		LongLived:       true,
		ErrorStrategy:   domain.ImportErrorStrategyAbort,
		RequestID:       "request-42",
		DispatchToken:   dispatchToken,
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
		"dispatchToken": "opaque-dispatch-token"
	}`, string(payload))
	require.NotContains(t, string(payload), objectKeyCanary)
	require.NotContains(t, string(payload), "sourceObjectKey")
	require.NotContains(t, string(payload), claimToken)
	require.NotContains(t, string(payload), "claimToken")

	var decoded coreapp.MicrosoftImportTask
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Empty(t, decoded.SourceObjectKey)
	require.Empty(t, decoded.ClaimToken)
	require.Equal(t, dispatchToken, decoded.DispatchToken)
}
