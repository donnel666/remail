package api

import (
	"context"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/stretchr/testify/require"
)

type historicalProjectMatcherStub struct {
	request mailapp.HistoricalProjectMatchRequest
}

func (s *historicalProjectMatcherStub) MatchMicrosoftHistory(_ context.Context, req mailapp.HistoricalProjectMatchRequest) error {
	s.request = req
	return nil
}

func TestResourceValidationHistoryMatcherReceivesFetchedMessages(t *testing.T) {
	matcher := &historicalProjectMatcherStub{}
	adapter := &ResourceValidationAdapter{history: matcher}
	receivedAt := time.Now().UTC()
	err := adapter.matchHistoricalProjects(context.Background(), coreapp.MicrosoftValidationRequest{
		ResourceID:   100,
		EmailAddress: "MAIN@example.com",
	}, mailinfra.MicrosoftOAuthResult{
		Valid: true,
		MailFetch: &mailinfra.MicrosoftMailFetchResult{
			Messages: []mailinfra.MicrosoftFetchedMessage{{
				ID:                "provider-1",
				InternetMessageID: "<message-1@example.com>",
				From:              "GitHub <noreply@github.com>",
				To:                "Main <main@example.com>",
				Subject:           "Welcome",
				Body:              "Account created.",
				Preview:           "Account created.",
				Protocol:          "graph",
				FolderLabel:       "inbox",
				ReceivedAt:        receivedAt,
			}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, uint(100), matcher.request.ResourceID)
	require.Equal(t, "main@example.com", matcher.request.EmailAddress)
	require.Len(t, matcher.request.Messages, 1)
	require.Equal(t, []string{"main@example.com"}, matcher.request.Messages[0].Recipients)
	require.WithinDuration(t, receivedAt, matcher.request.Messages[0].ReceivedAt, time.Millisecond)
}
