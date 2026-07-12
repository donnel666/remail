package api

import (
	"context"
	"errors"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/stretchr/testify/require"
)

type microsoftOAuthProtocolStub struct {
	result  mailinfra.MicrosoftOAuthResult
	err     error
	request mailinfra.MicrosoftOAuthRequest
	calls   int
}

func (s *microsoftOAuthProtocolStub) RefreshToken(_ context.Context, request mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
	s.calls++
	s.request = request
	return s.result, s.err
}

func (s *microsoftOAuthProtocolStub) AcquireToken(context.Context, mailinfra.MicrosoftOAuthRequest) (mailinfra.MicrosoftOAuthResult, error) {
	return mailinfra.MicrosoftOAuthResult{}, nil
}

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

func TestMicrosoftTokenRefreshACLReturnsRotatedCredentialsButNeverAccessToken(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
		Valid:        true,
		ClientID:     "rotated-client-id",
		RefreshToken: "rotated-refresh-token",
		AccessToken:  "access-token-canary",
	}}
	adapter := &ResourceValidationAdapter{microsoft: oauth}
	result, err := adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{
		ResourceID:   42,
		EmailAddress: "main@example.com",
		ClientID:     "original-client-id",
		RefreshToken: "original-refresh-token",
		RequestID:    "request-42",
	})
	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, "rotated-client-id", result.ClientID)
	require.Equal(t, "rotated-refresh-token", result.RefreshToken)
	require.Equal(t, "Microsoft refresh-token diagnostic succeeded.", result.SafeMessage)
	require.Equal(t, "original-client-id", oauth.request.ClientID)
	require.Equal(t, "original-refresh-token", oauth.request.RefreshToken)
}

func TestMicrosoftTokenRefreshACLUsesFixedSafeFailureMessages(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{result: mailinfra.MicrosoftOAuthResult{
		Category:    "oauth_invalid_grant",
		SafeMessage: "raw upstream body refresh-token-canary access-token-canary",
	}}
	adapter := &ResourceValidationAdapter{microsoft: oauth}
	result, err := adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{
		EmailAddress: "main@example.com",
		ClientID:     "client-canary",
		RefreshToken: "refresh-token-canary",
	})
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "oauth_invalid_grant", result.Category)
	require.Equal(t, "Microsoft refresh token is invalid or expired.", result.SafeMessage)
	require.NotContains(t, result.SafeMessage, "canary")

	oauth.result = mailinfra.MicrosoftOAuthResult{
		Category:     "unrecognized-upstream-category",
		SafeMessage:  "database and token internals",
		ProxyFailure: true,
	}
	result, err = adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{})
	require.NoError(t, err)
	require.Equal(t, maxMicrosoftProxyAttempts+2, oauth.calls)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "Microsoft mail service is temporarily unavailable.", result.SafeMessage)
}

func TestMicrosoftTokenRefreshACLConvertsProtocolErrorsToSafeRetryableResult(t *testing.T) {
	oauth := &microsoftOAuthProtocolStub{err: errors.New("upstream response contained refresh-token-canary")}
	adapter := &ResourceValidationAdapter{microsoft: oauth}
	result, err := adapter.RefreshMicrosoftToken(context.Background(), mailapp.MicrosoftTokenRefreshProtocolRequest{})
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "Microsoft mail service is temporarily unavailable.", result.SafeMessage)
	require.NotContains(t, result.SafeMessage, "canary")
}
