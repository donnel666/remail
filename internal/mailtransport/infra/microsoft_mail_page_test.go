package infra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftGraphPageContinuesBeyondKnownHeadAndReadLimit(t *testing.T) {
	now := time.Now().UTC()
	var visited []string
	client := &MicrosoftMailFetchClient{
		newGraphSession: func(context.Context, string, int) (*msacl.Session, error) { return nil, nil },
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(_ context.Context, _ *msacl.Session, _ string, folder MicrosoftMailFolder, req MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			visited = append(visited, folder.Label)
			require.Equal(t, 2, req.Page.Limit)
			require.False(t, req.MetadataOnly)
			require.Nil(t, req.OnMessages)
			if req.Page.Cursor != "" {
				require.Equal(t, "Inbox", folder.Label)
				require.Equal(t, microsoftGraphAPIBase+"/me/mailFolders/inbox/messages?$skip=2", req.Page.Cursor)
				return microsoftFolderFetchResult{Count: 1, Messages: []MicrosoftFetchedMessage{{ID: "target", FolderLabel: folder.Label, ReceivedAt: now.Add(-time.Minute)}}}, nil
			}
			result := microsoftFolderFetchResult{Count: 2, Messages: []MicrosoftFetchedMessage{
				{ID: "head", InternetMessageID: "reused@example.test", FolderLabel: folder.Label, Protocol: "graph", ReceivedAt: now},
				{ID: "second", InternetMessageID: "reused@example.test", FolderLabel: folder.Label, Protocol: "graph", ReceivedAt: now.Add(-time.Second)},
			}}
			if folder.Label == "Inbox" {
				result.NextLink = microsoftGraphAPIBase + "/me/mailFolders/inbox/messages?$skip=2"
			}
			return result, nil
		},
	}
	req := MicrosoftMailFetchRequest{
		EmailAddress: "owner@example.test", ClientID: "client", RefreshToken: "refresh", AccessToken: "access",
		MaxMessages: 1, StopAfterLimit: true, Page: &mailbox.PageRequest{Limit: 2},
		KnownMessageIDs: []string{"internet:reused@example.test", "provider:graph:inbox:head"},
	}
	first, err := client.FetchGraphOnly(context.Background(), req)
	require.NoError(t, err)
	require.True(t, first.Valid)
	require.Len(t, first.Messages, 4, "candidate pages cannot be truncated to the order's one-code limit")
	require.False(t, first.Page.Complete)
	// A completed page from the other protocol must retain Graph's unfinished folder.
	other := readMicrosoftMailPageCursor(first.Page.NextCursor, "imap")
	other.IMAP["INBOX"] = mailbox.IMAPPageCursor{UIDValidity: 1, BeforeUID: 20}
	req.Page.Cursor = microsoftMailPageResult(other, false).NextCursor
	second, err := client.FetchGraphOnly(context.Background(), req)
	require.NoError(t, err)
	require.True(t, second.Page.Complete)
	require.Len(t, second.Messages, 1)
	require.Equal(t, "target", second.Messages[0].ID)
	require.Equal(t, []string{"Inbox", "Junk", "Inbox"}, visited)
}

func TestMicrosoftGraphPageFailureDoesNotPublishPartialProgress(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		newGraphSession: func(context.Context, string, int) (*msacl.Session, error) { return nil, nil },
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(_ context.Context, _ *msacl.Session, _ string, folder MicrosoftMailFolder, _ MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			if folder.Label == "Junk" {
				return microsoftFolderFetchResult{}, errors.New("connection lost")
			}
			return microsoftFolderFetchResult{Count: 1, Messages: []MicrosoftFetchedMessage{{ID: "uncommitted"}}}, nil
		},
	}
	result, err := client.FetchGraphOnly(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "owner@example.test", ClientID: "client", RefreshToken: "latest-refresh", AccessToken: "access",
		Page: &mailbox.PageRequest{Limit: 2}, MaxMessages: 1,
	})
	require.Error(t, err)
	require.False(t, result.Valid)
	require.Nil(t, result.Page)
	require.Empty(t, result.Messages)
	require.Equal(t, "latest-refresh", result.RefreshToken)
}

func TestMicrosoftGraphPageBudgetAndCursorOrigin(t *testing.T) {
	raw := graphFolderMessagesURL(defaultMicrosoftMailFolders[0], MicrosoftMailFetchRequest{
		MaxMessages: 1, Page: &mailbox.PageRequest{Limit: 47},
	})
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "47", u.Query().Get("$top"))
	require.True(t, validMicrosoftGraphPageURL(raw))
	for _, invalid := range []string{
		"http://graph.microsoft.com/v1.0/me/messages", "https://graph.microsoft.com.evil.test/v1.0/me/messages",
		"https://graph.microsoft.com@evil.test/v1.0/me/messages", "https://graph.microsoft.com:444/v1.0/me/messages",
		"https://graph.microsoft.com/v1.0/me/messages#fragment", "/v1.0/me/messages",
	} {
		require.False(t, validMicrosoftGraphPageURL(invalid), invalid)
	}
}

func TestMicrosoftMessageIDAloneCannotHideAnotherOriginal(t *testing.T) {
	message := MicrosoftFetchedMessage{ID: "new-provider-id", Protocol: "graph", FolderLabel: "Inbox", InternetMessageID: "reused@example.test"}
	require.False(t, microsoftMessageIdentityKnown(message, []string{"internet:reused@example.test", "provider:graph:inbox:old-provider-id"}))
	require.True(t, microsoftMessageIdentityKnown(message, []string{"provider:graph:inbox:new-provider-id"}))
}

func microsoftPageTestIMAP(t *testing.T) (*imapclient.Client, *imapmemserver.User) {
	t.Helper()
	memory := imapmemserver.New()
	user := imapmemserver.NewUser("fixture", "fixture")
	require.NoError(t, user.Create("INBOX", nil))
	require.NoError(t, user.Create("Junk", &imap.CreateOptions{SpecialUse: []imap.MailboxAttr{imap.MailboxAttrJunk}}))
	memory.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memory.NewSession(), nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}}, InsecureAuth: true,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	client := imapclient.New(conn, nil)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Login("fixture", "fixture").Wait())
	return client, user
}

func appendMicrosoftPageMail(t *testing.T, client *imapclient.Client, folder string, index int, received time.Time) {
	t.Helper()
	recipient := "main@example.test"
	if index == 0 {
		recipient = "alias@example.test"
	}
	raw := fmt.Sprintf("From: sender@example.test\r\nTo: %s\r\nSubject: message %d\r\nMessage-ID: <same@example.test>\r\n\r\ncode 654321", recipient, index)
	command := client.Append(folder, int64(len(raw)), &imap.AppendOptions{Time: received})
	_, err := command.Write([]byte(raw))
	require.NoError(t, err)
	require.NoError(t, command.Close())
	_, err = command.Wait()
	require.NoError(t, err)
}

func TestMicrosoftIMAPPageReachesDeepCandidateAndRestartsOnUIDValidityChange(t *testing.T) {
	client, user := microsoftPageTestIMAP(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i := range 33 {
		appendMicrosoftPageMail(t, client, "INBOX", i, now.Add(time.Duration(i-33)*time.Second))
	}
	appendMicrosoftPageMail(t, client, "Junk", 99, now.Add(-time.Second))
	req := MicrosoftMailFetchRequest{
		SinceAt: now.Add(-time.Hour), UntilAt: now, MaxMessages: 1, StopAfterLimit: true,
		KnownMessageIDs: []string{"internet:same@example.test"}, Page: &mailbox.PageRequest{Limit: 10},
	}
	fetch := func() MicrosoftMailFetchResult {
		result, err := fetchMicrosoftIMAPPage(context.Background(), client,
			[]MicrosoftMailFolder{{ID: "INBOX", Label: "Inbox"}, {ID: "Junk", Label: "Junk"}}, req,
			MicrosoftMailFetchResult{Valid: true, Protocol: "imap", FolderCounts: map[string]int{}})
		require.NoError(t, err)
		require.True(t, result.Valid)
		require.NotNil(t, result.Page)
		req.Page.Cursor = result.Page.NextCursor
		return result
	}
	var all []MicrosoftFetchedMessage
	for page := range 4 {
		result := fetch()
		require.Equal(t, page == 3, result.Page.Complete)
		all = append(all, result.Messages...)
		if page == 0 {
			_, err := client.Select("INBOX", nil).Wait()
			require.NoError(t, err)
			require.NoError(t, client.Store(imap.UIDSetNum(33), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close())
			_, err = client.Expunge().Collect()
			require.NoError(t, err)
		}
	}
	require.Len(t, all, 34)
	seen := make(map[string]bool)
	foundTarget := false
	for _, message := range all {
		identity := message.FolderID + ":" + message.ID
		require.False(t, seen[identity], "pages must not restart at the same head")
		seen[identity] = true
		foundTarget = foundTarget || strings.Contains(message.To, "alias@example.test")
	}
	require.True(t, foundTarget, "the alias message is deeper than the previous 30-message cache")
	require.NoError(t, user.Delete("INBOX"))
	require.NoError(t, user.Create("INBOX", nil))
	appendMicrosoftPageMail(t, client, "INBOX", 100, now.Add(-time.Second))
	restarted := fetch()
	require.True(t, restarted.Page.Complete)
	require.Len(t, restarted.Messages, 1, "a completed old UIDVALIDITY cannot hide a recreated mailbox")
	require.Equal(t, "message 100", restarted.Messages[0].Subject)
}

func TestMicrosoftIMAPPageIncludesWindowAcrossProviderTimezones(t *testing.T) {
	client, _ := microsoftPageTestIMAP(t)
	since := time.Date(2026, 10, 18, 0, 30, 0, 0, time.UTC)
	until := time.Date(2026, 10, 18, 23, 0, 0, 0, time.UTC)
	appendMicrosoftPageMail(t, client, "INBOX", 0, until.Add(-time.Hour).In(time.FixedZone("Korea", 9*60*60)))
	appendMicrosoftPageMail(t, client, "INBOX", 1, since.Add(30*time.Minute).In(time.FixedZone("West", -12*60*60)))
	appendMicrosoftPageMail(t, client, "INBOX", 2, since.Add(-time.Second))
	appendMicrosoftPageMail(t, client, "INBOX", 3, until.Add(time.Second))
	result, err := fetchMicrosoftIMAPPage(context.Background(), client,
		[]MicrosoftMailFolder{{ID: "INBOX", Label: "Inbox"}, {ID: "Junk", Label: "Junk"}},
		MicrosoftMailFetchRequest{SinceAt: since, UntilAt: until, Page: &mailbox.PageRequest{Limit: 10}},
		MicrosoftMailFetchResult{Valid: true, Protocol: "imap", FolderCounts: map[string]int{}})
	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, result.Page.Complete)
	require.Len(t, result.Messages, 2)
	require.Equal(t, "message 0", result.Messages[0].Subject)
	require.Equal(t, "message 1", result.Messages[1].Subject)
}
