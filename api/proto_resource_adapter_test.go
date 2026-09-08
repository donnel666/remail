package api

import (
	"context"
	"errors"
	"testing"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	protodomain "github.com/donnel666/remail/internal/proto/domain"
	protoinfra "github.com/donnel666/remail/internal/proto/infra"
	"github.com/donnel666/remail/internal/proto/infra/proton"
	"github.com/stretchr/testify/require"
)

type protoMailboxFunc func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error)

func (f protoMailboxFunc) FetchMailbox(ctx context.Context, id uint, revision uint64, req proton.FetchRequest) (proton.FetchResult, error) {
	return f(ctx, id, revision, req)
}

func TestProtoMailFetchAdapterCarriesFenceAndOriginalRecipients(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	req := mailmatchapp.FetchMessagesRequest{
		Scope:   mailmatchapp.OrderScope{AllocationType: mailmatchdomain.ResourceTypeProto, EmailResourceID: 42, CredentialRevision: 7, Recipient: "a.b@proton.me"},
		SinceAt: now.Add(-time.Hour), UntilAt: now, MaxMessages: 10, KnownMessageIDs: []string{"known"},
	}
	streamed := 0
	req.OnMessages = func(messages []mailmatchapp.FetchedMessage) { streamed += len(messages) }
	message := proton.Message{ID: "message-id", Subject: "Login code", Body: "Your code is 123456", Folder: "Inbox", ExternalID: "<external-id>",
		Sender: proton.Address{Address: "security@example.com"}, ToList: []proton.Address{{Address: req.Scope.Recipient}}, OriginalToCount: 1, ReceivedAt: now}
	adapter := protoMailFetchAdapter{resources: protoMailboxFunc(func(_ context.Context, id uint, revision uint64, fetch proton.FetchRequest) (proton.FetchResult, error) {
		require.Equal(t, uint(42), id)
		require.Equal(t, uint64(7), revision)
		require.Equal(t, req.SinceAt, fetch.SinceAt)
		require.Equal(t, req.UntilAt, fetch.UntilAt)
		require.Equal(t, req.MaxMessages, fetch.MaxMessages)
		require.Equal(t, req.KnownMessageIDs, fetch.KnownMessageIDs)
		require.Nil(t, fetch.OnSession, "public MailMatch adapters must never receive session secrets")
		require.NoError(t, fetch.OnMessages([]proton.Message{message}))
		duplicate := message
		duplicate.OriginalToCount = 2
		return proton.FetchResult{Complete: true, Messages: []proton.Message{message, duplicate}}, nil
	})}
	result, err := adapter.FetchProtoMessages(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, streamed)
	require.Len(t, result.Messages, 2)
	item := result.Messages[0]
	require.Equal(t, mailmatchdomain.ResourceTypeProto, item.ResourceType)
	require.Equal(t, uint64(7), item.CredentialRevision)
	require.Equal(t, []string{"a.b@proton.me"}, item.Recipients)
	require.Equal(t, item.Recipients, item.ToRecipients)
	require.Empty(t, result.Messages[1].ToRecipients)
	require.Equal(t, "external-id", item.MessageIDHeader)
	require.Equal(t, "security@example.com", item.Sender)
	require.Equal(t, "proton", item.Protocol)
	require.Equal(t, now, item.ReceivedAt)
}

func TestProtoMailFetchAdapterRejectsPartialAndSanitizesFailure(t *testing.T) {
	req := mailmatchapp.FetchMessagesRequest{Scope: mailmatchapp.OrderScope{AllocationType: mailmatchdomain.ResourceTypeProto, EmailResourceID: 42, CredentialRevision: 7, Recipient: "one@proton.me"}, FullHistory: true}
	for _, tc := range []struct {
		name     string
		err      error
		category string
		want     error
	}{
		{"partial history", nil, "incomplete_history", nil},
		{"stale revision", protodomain.ErrInvalidClaim, "", mailmatchdomain.ErrResourceFetchCredentialChanged},
		{"session busy", protoinfra.ErrSessionBusy, "session_busy", nil},
		{"missing session", protoinfra.ErrSessionUnavailable, "session_unavailable", nil},
		{"upstream", &proton.Failure{Category: "rate_limited", SafeMessage: "Proto service is rate limited.", Retryable: true, Cause: errors.New("secret-token-value")}, "rate_limited", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := protoMailFetchAdapter{resources: protoMailboxFunc(func(context.Context, uint, uint64, proton.FetchRequest) (proton.FetchResult, error) {
				return proton.FetchResult{}, tc.err
			})}
			result, err := adapter.FetchProtoMessages(context.Background(), req)
			require.Nil(t, result)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-token-value")
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				return
			}
			var failure *mailmatchapp.MailFetchFailure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, tc.category, failure.Category)
			require.True(t, failure.Retryable)
		})
	}
}
