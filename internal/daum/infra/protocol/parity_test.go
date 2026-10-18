package protocol

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDaumUsesMicrosoftHTMLPreferenceAndPreservesOriginalToCount(t *testing.T) {
	raw := "From: sender@example.test\r\nTo: one@daum.net, one@daum.net\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=parts\r\n\r\n" +
		"--parts\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOpen the HTML version.\r\n" +
		"--parts\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Your code is 847291</p>\r\n--parts--\r\n"
	message, err := parseMessage([]byte(raw), "imap:test", "INBOX", time.Now())
	require.NoError(t, err)
	require.Equal(t, "<p>Your code is 847291</p>", message.Body)
	require.Equal(t, "text/html", message.MIMEType)
	require.Equal(t, 2, message.OriginalToCount)
	require.Len(t, message.ToList, 1)
}

func TestDaumRealtimeStopsAtKnownNewestWithoutDownloadingBodies(t *testing.T) {
	oldMail := strings.ReplaceAll(koreanMail, "same-message@example.test", "older@example.test")
	newMail := strings.ReplaceAll(koreanMail, "same-message@example.test", "newest@example.test")
	var commands []string
	client, _ := scriptedClient(t, imapScript(&commands, false, oldMail, newMail))
	result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "synthetic", Protocol: "imap"}, FetchRequest{MaxMessages: 1, KnownMessageIDs: []string{"provider:imap:inbox:imap:inbox:99:2"}})
	require.NoError(t, err)
	require.Empty(t, result.Messages)
	require.True(t, result.Complete)
	require.NotContains(t, strings.Join(commands, "\n"), "BODY.PEEK")
}

func TestDaumRecentLimitCombinesFoldersBeforeApplyingBoundary(t *testing.T) {
	now := time.Now()
	result := FetchResult{Messages: []Message{{ID: "imap:inbox:1", Folder: "INBOX", ReceivedAt: now.Add(-time.Minute)}, {ID: "imap:junk:2", Folder: "Spam", ExternalID: "newest", ReceivedAt: now}}}
	require.NoError(t, finishRecentFetch(&result, FetchRequest{MaxMessages: 1}))
	require.Len(t, result.Messages, 1)
	require.Equal(t, "imap:junk:2", result.Messages[0].ID)
	require.NoError(t, finishRecentFetch(&result, FetchRequest{MaxMessages: 1, KnownMessageIDs: []string{"imap:junk:2"}}))
	require.Empty(t, result.Messages)
}

func TestDaumHistorySkipsSentAndReadsMessagesAboveFormerLimit(t *testing.T) {
	raw := "From: sender@example.test\r\nTo: account@daum.net\r\nSubject: Large message\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("x", 9<<20)
	var commands []string
	client, _ := scriptedClient(t, imapScript(&commands, false, raw))
	result, err := client.Fetch(context.Background(), Credentials{Email: "account@daum.net", AppPassword: "synthetic", Protocol: "imap"}, FetchRequest{FullHistory: true})
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	require.Len(t, result.Messages[0].Body, 9<<20)
	require.NotContains(t, strings.Join(commands, "\n"), "EXAMINE Sent")
}
