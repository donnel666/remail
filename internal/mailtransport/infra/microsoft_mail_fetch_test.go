package infra

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMicrosoftIMAPClient struct {
	called      bool
	accessToken string
	result      MicrosoftMailFetchResult
	err         error
}

func (c *fakeMicrosoftIMAPClient) FetchAll(_ context.Context, _ MicrosoftMailFetchRequest, accessToken string) (MicrosoftMailFetchResult, error) {
	c.called = true
	c.accessToken = accessToken
	return c.result, c.err
}

func TestMicrosoftMailFetchClientGraphSuccessDoesNotFallback(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(_ context.Context, req MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			assert.Equal(t, "user@example.com", req.EmailAddress)
			assert.Equal(t, defaultMicrosoftClientID, req.ClientID)
			return MicrosoftMailFetchResult{
				Valid:        true,
				Protocol:     "graph",
				RefreshToken: "rotated-rt",
				MessageCount: 2,
				FolderCounts: map[string]int{"Inbox": 1, "Junk": 1},
			}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		RefreshToken: "old-rt",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	assert.Equal(t, "graph", result.Protocol)
	assert.Equal(t, "rotated-rt", result.RefreshToken)
	assert.Equal(t, 2, result.MessageCount)
	assert.False(t, imapFallback.called)
}

func TestMicrosoftMailFetchClientFallsBackToIMAPAfterGraphFailure(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", true), errors.New("graph unavailable")
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{
		result: MicrosoftMailFetchResult{
			Valid:        true,
			Protocol:     "imap",
			MessageCount: 3,
			FolderCounts: map[string]int{"Inbox": 2, "Junk": 1},
		},
	}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		ClientID:     "client-id",
		RefreshToken: "old-rt",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	assert.True(t, imapFallback.called)
	assert.Equal(t, "imap-access-token", imapFallback.accessToken)
	assert.Equal(t, "imap", result.Protocol)
	assert.Equal(t, "graph", result.FallbackFrom)
	assert.Equal(t, "imap-rotated-rt", result.RefreshToken)
	assert.Equal(t, "Microsoft mail service is temporarily unavailable.", result.GraphSafeError)
}
