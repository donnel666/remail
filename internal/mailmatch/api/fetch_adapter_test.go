package api

import (
	"context"
	"errors"
	"testing"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/stretchr/testify/require"
)

type microsoftMessageFetchClientStub struct {
	requests []mailinfra.MicrosoftMailFetchRequest
	results  []mailinfra.MicrosoftMailFetchResult
}

func (s *microsoftMessageFetchClientStub) FetchAll(_ context.Context, req mailinfra.MicrosoftMailFetchRequest) (mailinfra.MicrosoftMailFetchResult, error) {
	s.requests = append(s.requests, req)
	index := len(s.requests) - 1
	if index >= len(s.results) {
		return mailinfra.MicrosoftMailFetchResult{}, nil
	}
	return s.results[index], nil
}

func TestMicrosoftFetchAdapterRetriesWithLatestRotatedRefreshToken(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{
		{
			Category:     "request",
			SafeMessage:  "Microsoft mail service is temporarily unavailable.",
			ProxyFailure: true,
			RefreshToken: "rotated-after-first-attempt",
		},
		{
			Valid:        true,
			Protocol:     "graph",
			RefreshToken: "rotated-after-second-attempt",
		},
	}}
	adapter := &MicrosoftFetchAdapter{client: client}

	result, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail:    "owner@example.test",
			MicrosoftClientID: "client-id",
			MicrosoftRT:       "original-refresh-token",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "rotated-after-second-attempt", result.RefreshToken)
	require.Len(t, client.requests, 2)
	require.Equal(t, "original-refresh-token", client.requests[0].RefreshToken)
	require.Equal(t, "rotated-after-first-attempt", client.requests[1].RefreshToken)
}

func TestMicrosoftFetchAdapterFullHistoryHasNoMessageLimit(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{{Valid: true}}}
	adapter := &MicrosoftFetchAdapter{client: client}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "refresh-token",
		},
		FullHistory: true,
	})

	require.NoError(t, err)
	require.Len(t, client.requests, 1)
	require.Zero(t, client.requests[0].MaxMessages)
}

func TestMicrosoftFetchAdapterReturnsRotatedTokenOnFetchFailure(t *testing.T) {
	client := &microsoftMessageFetchClientStub{results: []mailinfra.MicrosoftMailFetchResult{{
		Category: "graph_forbidden", SafeMessage: "Mailbox permission is unavailable.", RefreshToken: "rotated-refresh-token",
	}}}
	adapter := &MicrosoftFetchAdapter{client: client}

	_, err := adapter.FetchMicrosoftMessages(context.Background(), mailmatchapp.FetchMessagesRequest{
		Scope: mailmatchapp.OrderScope{
			MicrosoftEmail: "owner@example.test", MicrosoftClientID: "client-id", MicrosoftRT: "original-refresh-token",
		},
	})

	var failure *mailmatchapp.MailFetchFailure
	require.True(t, errors.As(err, &failure))
	require.Equal(t, "rotated-refresh-token", failure.RefreshToken)
}
