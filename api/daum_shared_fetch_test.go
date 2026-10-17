package api

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/daum/infra/protocol"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

func TestDaumOrderFetchFillsTheSharedAccountCacheForAllAliases(t *testing.T) {
	request := mailmatchapp.FetchMessagesRequest{Scope: mailmatchapp.OrderScope{
		AllocationType: mailmatchdomain.ResourceTypeDaum, EmailResourceID: 8,
		CredentialRevision: 3, Recipient: "one@hanmail2.net",
	}, MaxMessages: 30}
	adapter := daumMailFetchAdapter{resources: daumMailboxFunc(func(_ context.Context, _ uint, _ uint64, req protocol.FetchRequest) (protocol.FetchResult, error) {
		require.Empty(t, req.Recipient)
		require.Equal(t, 30, req.MaxMessages)
		return protocol.FetchResult{Complete: true, Messages: []protocol.Message{
			{ID: "imap:INBOX:1:1", ToList: []protocol.Address{{Address: "one@daum.net"}}},
			{ID: "imap:INBOX:1:2", ToList: []protocol.Address{{Address: "one@hanmail.net"}}},
			{ID: "imap:INBOX:1:3", ToList: []protocol.Address{{Address: "one@hanmail2.net"}}},
		}}, nil
	})}
	result, err := adapter.FetchDaumMessages(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, result.Messages, 3)
	require.Equal(t, "one@daum.net", result.Messages[0].Recipient)
	require.Equal(t, "one@hanmail.net", result.Messages[1].Recipient)
	require.Equal(t, "one@hanmail2.net", result.Messages[2].Recipient)
}
