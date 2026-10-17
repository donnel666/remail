package protocol

import (
	"bytes"
	"context"
	"fmt"
	stdmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/stretchr/testify/require"
)

func orderingMail(uid uint32, id string, receivedAt time.Time) imapScriptMessage {
	return imapScriptMessage{
		uid: uid, receivedAt: receivedAt,
		raw: fmt.Sprintf("From: sender@example.test\r\nTo: user@daum.net\r\nSubject: %s\r\nMessage-ID: <%s@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nYour code is 847291\r\n", id, id),
	}
}

func TestDaumRealtimeAscendingFetchRepliesKeepNewest(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		known     []string
		recipient string
	}{
		{name: "initial_fetch"},
		{name: "older_message_known", known: []string{"internet:m1@example.test"}},
		{name: "recipient_filter_reads_all_candidates", known: []string{"internet:m1@example.test"}, recipient: "user@daum.net"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var commands []string
			client, _ := scriptedClient(t, imapMailboxScript(&commands, false, false, map[string][]imapScriptMessage{
				"INBOX": {orderingMail(1, "m1", now.Add(-time.Minute)), orderingMail(2, "m2", now)}, "Spam": nil,
			}))
			result, err := client.Fetch(context.Background(), Credentials{Email: "user@daum.net", AppPassword: "synthetic", Protocol: "imap"}, FetchRequest{
				MaxMessages: 1, KnownMessageIDs: test.known, Recipient: test.recipient, SinceAt: now.Add(-time.Hour),
			})
			require.NoError(t, err)
			require.True(t, result.Complete)
			require.Len(t, result.Messages, 1)
			require.Equal(t, "m2@example.test", result.Messages[0].ExternalID)
		})
	}
}

func TestDaumRealtimeCombinesAscendingFolderRepliesBeforeStreaming(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var commands []string
	client, _ := scriptedClient(t, imapMailboxScript(&commands, false, false, map[string][]imapScriptMessage{
		"INBOX": {orderingMail(1, "inbox-old", now.Add(-3*time.Minute)), orderingMail(2, "inbox-new", now.Add(-time.Minute))},
		"Spam":  {orderingMail(1, "spam-old", now.Add(-2*time.Minute)), orderingMail(2, "spam-new", now)},
	}))
	var streamed []Message
	var callbacks int
	result, err := client.Fetch(context.Background(), Credentials{Email: "user@daum.net", AppPassword: "synthetic", Protocol: "imap"}, FetchRequest{
		MaxMessages: 2,
		OnMessages: func(messages []Message) error {
			callbacks++
			streamed = append(streamed, messages...)
			return nil
		},
	})
	require.NoError(t, err)
	require.True(t, result.Complete)
	require.Empty(t, result.Messages)
	require.Equal(t, 1, callbacks)
	require.Len(t, streamed, 2)
	require.Equal(t, "spam-new@example.test", streamed[0].ExternalID)
	require.Equal(t, "inbox-new@example.test", streamed[1].ExternalID)
}

func TestDaumIMAPSortUsesArrivalForHeadsAndBodies(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		known []string
		empty bool
	}{
		{name: "initial_fetch"},
		{name: "higher_uid_is_known_imported_old_mail", known: []string{"internet:m2@example.test"}},
		{name: "actual_newest_is_known", known: []string{"internet:m1@example.test"}, empty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var commands []string
			client, _ := scriptedClient(t, imapMailboxScript(&commands, false, true, map[string][]imapScriptMessage{
				"INBOX": {orderingMail(1, "m1", now), orderingMail(2, "m2", now.Add(-24*time.Hour))}, "Spam": nil,
			}))
			result, err := client.Fetch(context.Background(), Credentials{Email: "user@daum.net", AppPassword: "synthetic", Protocol: "imap"}, FetchRequest{
				MaxMessages: 1, KnownMessageIDs: test.known,
			})
			require.NoError(t, err)
			require.True(t, result.Complete)
			require.Contains(t, strings.Join(commands, "\n"), "SORT (REVERSE ARRIVAL)")
			if test.empty {
				require.Empty(t, result.Messages)
				require.NotContains(t, strings.Join(commands, "\n"), "BODY.PEEK")
			} else {
				require.Len(t, result.Messages, 1)
				require.Equal(t, "m1@example.test", result.Messages[0].ExternalID)
			}
		})
	}
}

func TestDaumUnknownCharsetMatchesMicrosoftMIMEFallback(t *testing.T) {
	raw := []byte("From: sender@example.test\r\nTo: user@daum.net\r\nSubject: code\r\nContent-Type: text/plain; charset=x-custom-provider\r\n\r\nYour code is 847291")
	source, err := stdmail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	body, _, _, err := mailbox.ReadMIMEBodyPart(source.Header.Get("Content-Type"), "", source.Body)
	require.NoError(t, err)
	require.Contains(t, body, "847291")
	parsed, err := parseMessage(raw, "imap:INBOX:99:1", "INBOX", time.Now())
	require.NoError(t, err)
	require.Equal(t, body, parsed.Body)
}

func TestDaumMalformedMessageContentDoesNotRejectMailbox(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed_source":          "not a mail header\r\n\r\ncontent",
		"unknown_transfer_encoding": "Content-Type: text/plain\r\nContent-Transfer-Encoding: x-custom-transfer\r\n\r\ncode 847291",
		"invalid_base64":            "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\ninvalid-###",
		"broken_multipart":          "Content-Type: multipart/alternative; boundary=missing\r\n\r\nno boundary",
	} {
		t.Run(name, func(t *testing.T) {
			var commands []string
			client, _ := scriptedClient(t, imapScript(&commands, false, raw))
			result, err := client.Fetch(context.Background(), Credentials{Email: "user@daum.net", AppPassword: "synthetic", Protocol: "imap"}, FetchRequest{FullHistory: true})
			require.NoError(t, err, "message content is not evidence that account authentication failed")
			require.True(t, result.Complete)
			require.Len(t, result.Messages, 1)
			require.Equal(t, "imap:INBOX:99:1", result.Messages[0].ID)
		})
	}
}
