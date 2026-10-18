package protocol

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/stretchr/testify/require"
)

func TestIMAPPageContinuesPastKnownHeadAndOrderReadLimit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	messages := make([]imapScriptMessage, 51)
	for i := range messages {
		messages[i] = orderingMail(uint32(i+1), fmt.Sprintf("mail%d", i), now.Add(time.Duration(i-60)*time.Second))
	}
	messages[0].raw = strings.ReplaceAll(messages[0].raw, "user@daum.net", "user@hanmail2.net")
	messages[20].raw = strings.ReplaceAll(messages[20].raw, "user@daum.net", "user@hanmail.net")
	var commands []string
	client, _ := scriptedClient(t, imapMailboxScript(&commands, false, false, map[string][]imapScriptMessage{"INBOX": messages, "Spam": nil}))
	request := FetchRequest{SinceAt: now.Add(-time.Hour), UntilAt: now, MaxMessages: 1,
		KnownMessageIDs: []string{"provider:imap:inbox:imap:inbox:99:51", "internet:mail0@example.test"}, Page: &mailbox.PageRequest{Limit: 10}}
	seen := make(map[string]Message)
	for page := 0; page < 6; page++ {
		result, err := client.Fetch(context.Background(), Credentials{Email: "user@daum.net", AppPassword: "fixture", Protocol: "imap"}, request)
		require.NoError(t, err)
		require.NotNil(t, result.Page)
		require.LessOrEqual(t, len(result.Messages), 10)
		for _, message := range result.Messages {
			require.NotContains(t, seen, message.ID)
			seen[message.ID] = message
		}
		require.Equal(t, page == 5, result.Page.Complete)
		request.Page.Cursor = result.Page.NextCursor
	}
	require.Len(t, seen, 51)
	require.Equal(t, "user@hanmail2.net", seen["imap:INBOX:99:1"].ToList[0].Address)
	require.Equal(t, "user@hanmail.net", seen["imap:INBOX:99:21"].ToList[0].Address)
}

func TestDaumMessageIDAloneCannotSuppressDifferentMessages(t *testing.T) {
	message := Message{ID: "imap:INBOX:99:4", ExternalID: "collision@example.test", Folder: "INBOX"}
	require.False(t, messageKnown([]string{"internet:collision@example.test"}, message))
	require.True(t, messageKnown([]string{"provider:imap:inbox:imap:inbox:99:4"}, message))
}

func TestPOP3PagesContinuePastOtherRecipientsAndKnownMessageIDs(t *testing.T) {
	var raws []string
	for index := 0; index < 35; index++ {
		raw := strings.ReplaceAll(koreanMail, "same-message@example.test", fmt.Sprintf("message%d@example.test", index))
		if index > 0 {
			raw = strings.ReplaceAll(raw, "account@hanmail2.net", "account@daum.net")
		}
		raws = append(raws, raw)
	}
	var commands []string
	client, _ := scriptedClient(t, pop3Script(&commands, raws...))
	request := FetchRequest{MaxMessages: 1, KnownMessageIDs: []string{"internet:message0@example.test", "provider:pop3:inbox:pop3:stable-35"}, Page: &mailbox.PageRequest{Limit: 10}}
	var received []Message
	for page := 0; page < 4; page++ {
		result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "fixture", Protocol: "pop3"}, request)
		require.NoError(t, err)
		require.LessOrEqual(t, len(result.Messages), 10)
		received = append(received, result.Messages...)
		require.Equal(t, page == 3, result.Page.Complete)
		request.Page.Cursor = result.Page.NextCursor
	}
	require.Len(t, received, 35)
	require.Equal(t, "account@hanmail2.net", received[34].ToList[0].Address)
	require.NotContains(t, strings.Join(commands, "\n"), "DELE")
}
