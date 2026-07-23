package infra

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMicrosoftIMAPClient struct {
	called      bool
	accessToken string
	messages    []MicrosoftFetchedMessage
	result      MicrosoftMailFetchResult
	err         error
}

func (c *fakeMicrosoftIMAPClient) FetchAll(_ context.Context, req MicrosoftMailFetchRequest, accessToken string) (MicrosoftMailFetchResult, error) {
	c.called = true
	c.accessToken = accessToken
	if req.OnMessages != nil && len(c.messages) > 0 {
		req.OnMessages(c.messages)
	}
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

func TestMicrosoftMailFetchClientKeepsGraphRotatedTokenWhenIMAPExchangeFails(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			result := microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false)
			result.RefreshToken = "graph-rotated-rt"
			return result, nil
		},
		exchangeIMAPToken: func(_ context.Context, req MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			require.Equal(t, "graph-rotated-rt", req.RefreshToken)
			return "", "", microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false), errors.New("imap token exchange unavailable")
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		ClientID:     "client-id",
		RefreshToken: "old-rt",
	}, imapFallback)

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "graph-rotated-rt", result.RefreshToken)
	require.False(t, imapFallback.called)
}

func TestMicrosoftMailFetchClientGraphIdentityMismatchRequiresIMAPAccountProof(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure(microsoftIdentityMismatch, "Microsoft OAuth credentials do not match the configured account.", false), nil
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{result: MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "imap",
		FolderCounts: map[string]int{},
	}}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "alias@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, imapFallback.called)
	require.Equal(t, "imap", result.Protocol)
	require.Equal(t, "imap-rotated-rt", result.RefreshToken)
}

func TestMicrosoftMailFetchClientGraphIdentityMismatchSurvivesIMAPAuthFailure(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure(microsoftIdentityMismatch, "Microsoft OAuth credentials do not match the configured account.", false), nil
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{
		result: microsoftMailFetchFailure("imap_auth_failed", "Microsoft IMAP authentication failed.", false),
		err:    errors.New("imap authentication failed"),
	}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	}, imapFallback)

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, microsoftIdentityMismatch, result.Category)
	require.Equal(t, "Microsoft OAuth credentials do not match the configured account.", result.SafeMessage)
	require.Equal(t, "imap-rotated-rt", result.RefreshToken)
}

func TestMicrosoftMailFetchClientGraphIdentityMismatchKeepsTemporaryIMAPFailureRetryable(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(context.Context, MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			return microsoftMailFetchFailure(microsoftIdentityMismatch, "Microsoft OAuth credentials do not match the configured account.", false), nil
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{
		result: microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", true),
		err:    errors.New("temporary IMAP failure"),
	}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
	}, imapFallback)

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "imap-rotated-rt", result.RefreshToken)
}

func TestMicrosoftMailFetchClientChecksGraphIdentityBeforeFolders(t *testing.T) {
	folderCalls := 0
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMismatched, nil
		},
		graphFolderFetch: func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			folderCalls++
			return microsoftFolderFetchResult{}, nil
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, microsoftIdentityMismatch, result.Category)
	require.Equal(t, "refresh-token", result.RefreshToken)
	require.Zero(t, folderCalls)
}

func TestMicrosoftMailFetchClientEmptyGraphIdentityCannotReachFolders(t *testing.T) {
	folderCalls := 0
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityUnavailable, nil
		},
		graphFolderFetch: func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			folderCalls++
			return microsoftFolderFetchResult{}, nil
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Zero(t, folderCalls)
}

func TestMicrosoftMailFetchClientGraphFolderFailureKeepsRefreshToken(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			return microsoftFolderFetchResult{}, errors.New("temporary folder failure")
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "authoritative-refresh-token",
		AccessToken:  "access-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "authoritative-refresh-token", result.RefreshToken)
}

func TestMicrosoftMailFetchClientStreamsInboxAndJunk(t *testing.T) {
	var fetchedFolders []string
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(_ context.Context, _ *msacl.Session, _ string, folder MicrosoftMailFolder, req MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			req.OnMessages([]MicrosoftFetchedMessage{{FolderLabel: folder.Label}})
			return microsoftFolderFetchResult{Count: 1}, nil
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
		OnMessages: func(messages []MicrosoftFetchedMessage) {
			fetchedFolders = append(fetchedFolders, messages[0].FolderLabel)
		},
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, []string{"Inbox", "Junk"}, fetchedFolders)
	require.Equal(t, map[string]int{"Inbox": 1, "Junk": 1}, result.FolderCounts)
	require.Equal(t, 2, result.MessageCount)
}

func TestMicrosoftMailFetchClientReturnsNewestRealtimeMessagesAcrossInboxAndJunk(t *testing.T) {
	var folders []string
	now := time.Now().UTC()
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(_ context.Context, _ *msacl.Session, _ string, folder MicrosoftMailFolder, req MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			folders = append(folders, folder.Label)
			require.Equal(t, 6, req.MaxMessages)
			messages := make([]MicrosoftFetchedMessage, req.MaxMessages)
			for i := range messages {
				offset := i*2 + 1
				if folder.Label == "Junk" {
					offset = i * 2
				}
				messages[i] = MicrosoftFetchedMessage{
					ID: folder.Label + strconv.Itoa(i), FolderLabel: folder.Label,
					ReceivedAt: now.Add(-time.Duration(offset) * time.Minute),
				}
			}
			return microsoftFolderFetchResult{Count: len(messages), Messages: messages}, nil
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com", ClientID: "client-id", RefreshToken: "refresh-token",
		AccessToken: "access-token", MaxMessages: 6, StopAfterLimit: true,
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, []string{"Inbox", "Junk"}, folders)
	require.Equal(t, 6, result.MessageCount)
	require.Len(t, result.Messages, 6)
	require.Equal(t, []string{"Junk", "Inbox", "Junk", "Inbox", "Junk", "Inbox"}, []string{
		result.Messages[0].FolderLabel, result.Messages[1].FolderLabel, result.Messages[2].FolderLabel,
		result.Messages[3].FolderLabel, result.Messages[4].FolderLabel, result.Messages[5].FolderLabel,
	})
}

func TestMicrosoftMailFetchClientStopsAfterFolderHeadsWhenNewestMessageIsCached(t *testing.T) {
	now := time.Now().UTC()
	var limits []int
	var metadataOnly []bool
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(_ context.Context, _ *msacl.Session, _ string, folder MicrosoftMailFolder, req MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			limits = append(limits, req.MaxMessages)
			metadataOnly = append(metadataOnly, req.MetadataOnly)
			message := MicrosoftFetchedMessage{
				InternetMessageID: "older@example.com", FolderLabel: folder.Label,
				ReceivedAt: now.Add(-time.Minute), Protocol: "graph",
			}
			if folder.Label == "Inbox" {
				message.InternetMessageID = "cached@example.com"
				message.ReceivedAt = now
			}
			return microsoftFolderFetchResult{Count: 1, Messages: []MicrosoftFetchedMessage{message}}, nil
		},
	}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com", ClientID: "client-id", RefreshToken: "refresh-token",
		AccessToken: "access-token", MaxMessages: 30, StopAfterLimit: true,
		KnownMessageIDs: []string{"internet:cached@example.com"},
	}, &fakeMicrosoftIMAPClient{})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Empty(t, result.Messages)
	require.Zero(t, result.MessageCount)
	require.Equal(t, []int{1, 1}, limits)
	require.Equal(t, []bool{true, true}, metadataOnly)
}

func TestApplyMicrosoftMessageBoundaryReturnsOnlyMessagesNewerThanCachedID(t *testing.T) {
	now := time.Now().UTC()
	result := MicrosoftMailFetchResult{Messages: []MicrosoftFetchedMessage{
		{InternetMessageID: "old@example.com", ReceivedAt: now.Add(-2 * time.Minute)},
		{InternetMessageID: "new@example.com", ReceivedAt: now},
		{InternetMessageID: "cached@example.com", ReceivedAt: now.Add(-time.Minute)},
	}}

	applyMicrosoftMessageBoundary(&result, []string{"internet:cached@example.com"}, 30)

	require.Len(t, result.Messages, 1)
	require.Equal(t, "new@example.com", result.Messages[0].InternetMessageID)
	require.Equal(t, 1, result.MessageCount)
}

func TestMicrosoftMailFetchStreamsOnlyMessagesBeforeCachedBoundary(t *testing.T) {
	now := time.Now().UTC()
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(_ context.Context, _ *msacl.Session, _ string, folder MicrosoftMailFolder, req MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			if req.MetadataOnly {
				message := MicrosoftFetchedMessage{InternetMessageID: "junk-old@example.com", FolderLabel: folder.Label, ReceivedAt: now.Add(-time.Minute)}
				if folder.Label == "Inbox" {
					message.InternetMessageID = "new@example.com"
					message.ReceivedAt = now
				}
				return microsoftFolderFetchResult{Count: 1, Messages: []MicrosoftFetchedMessage{message}}, nil
			}
			if folder.Label == "Junk" {
				return microsoftFolderFetchResult{}, nil
			}
			messages := []MicrosoftFetchedMessage{
				{InternetMessageID: "new@example.com", FolderLabel: folder.Label, ReceivedAt: now},
				{InternetMessageID: "cached@example.com", FolderLabel: folder.Label, ReceivedAt: now.Add(-time.Minute)},
				{InternetMessageID: "older@example.com", FolderLabel: folder.Label, ReceivedAt: now.Add(-2 * time.Minute)},
			}
			return microsoftFolderFetchResult{Count: len(messages), Messages: messages}, nil
		},
	}
	var streamed []MicrosoftFetchedMessage

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com", ClientID: "client-id", RefreshToken: "refresh-token",
		AccessToken: "access-token", MaxMessages: 30, StopAfterLimit: true,
		KnownMessageIDs: []string{"internet:cached@example.com"},
		OnMessages:      func(messages []MicrosoftFetchedMessage) { streamed = append(streamed, messages...) },
	})

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Empty(t, result.Messages)
	require.Equal(t, 1, result.MessageCount)
	require.Len(t, streamed, 1)
	require.Equal(t, "new@example.com", streamed[0].InternetMessageID)
}

func TestLatestIMAPSeqSetBoundsRealtimeFolderRead(t *testing.T) {
	seqSet := latestIMAPSeqSet(1_000_000, 6)
	require.Equal(t, "999995:1000000", seqSet.String())
	nums, ok := seqSet.Nums()
	require.True(t, ok)
	require.Len(t, nums, 6)
}

func TestNewestMicrosoftMessagesKeepsGlobalNewestAcrossFolders(t *testing.T) {
	now := time.Now().UTC()
	messages := make([]MicrosoftFetchedMessage, 0, 12)
	for i := 0; i < 6; i++ {
		messages = append(messages,
			MicrosoftFetchedMessage{ID: "inbox-" + strconv.Itoa(i), FolderLabel: "Inbox", ReceivedAt: now.Add(-time.Duration(i*2+1) * time.Minute)},
			MicrosoftFetchedMessage{ID: "junk-" + strconv.Itoa(i), FolderLabel: "Junk", ReceivedAt: now.Add(-time.Duration(i*2) * time.Minute)},
		)
	}

	newest := newestMicrosoftMessages(messages, 6)
	require.Len(t, newest, 6)
	require.Equal(t, []string{"junk-0", "inbox-0", "junk-1", "inbox-1", "junk-2", "inbox-2"}, []string{
		newest[0].ID, newest[1].ID, newest[2].ID, newest[3].ID, newest[4].ID, newest[5].ID,
	})
}

func TestIMAPCandidateFoldersPreferSpecialUseJunk(t *testing.T) {
	folders := imapCandidateFoldersFromList([]*imap.ListData{
		{Mailbox: "INBOX"},
		{Mailbox: "Spam Archive"},
		{Mailbox: "Junk Email", Attrs: []imap.MailboxAttr{imap.MailboxAttrJunk}},
	})

	firstJunk := ""
	for _, folder := range folders {
		if folder.Label == "Junk" {
			firstJunk = folder.ID
			break
		}
	}
	require.Equal(t, "Junk Email", firstJunk)
}

func TestMicrosoftMailFetchClientJunkFailureInvalidatesInboxSuccess(t *testing.T) {
	var folderCalls []string
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityMatched, nil
		},
		graphFolderFetch: func(_ context.Context, _ *msacl.Session, _ string, folder MicrosoftMailFolder, _ MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error) {
			folderCalls = append(folderCalls, folder.Label)
			if folder.Label == "Junk" {
				return microsoftFolderFetchResult{}, errors.New("junk unavailable")
			}
			return microsoftFolderFetchResult{Count: 1}, nil
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
	})

	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, []string{"Inbox", "Junk"}, folderCalls)
}

func TestMicrosoftMailFetchClientResetsPartialGraphBeforeIMAP(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphFetch: func(_ context.Context, req MicrosoftMailFetchRequest) (MicrosoftMailFetchResult, error) {
			req.OnMessages([]MicrosoftFetchedMessage{{ID: "graph-inbox"}})
			return microsoftMailFetchFailure("request", "Graph Junk failed.", false), nil
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-refresh-token", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{
		messages: []MicrosoftFetchedMessage{{ID: "imap-inbox"}, {ID: "imap-junk"}},
		result: MicrosoftMailFetchResult{
			Valid: true, FolderCounts: map[string]int{"Inbox": 1, "Junk": 1}, MessageCount: 2,
		},
	}
	var messageIDs []string
	resets := 0

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		OnMessages: func(messages []MicrosoftFetchedMessage) {
			for _, message := range messages {
				messageIDs = append(messageIDs, message.ID)
			}
		},
		OnReset: func() {
			resets++
			messageIDs = nil
		},
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, 1, resets)
	require.Equal(t, []string{"imap-inbox", "imap-junk"}, messageIDs)
}

func TestRequiredMicrosoftMailFoldersCompleted(t *testing.T) {
	require.False(t, requiredMicrosoftMailFoldersCompleted(map[string]bool{"Inbox": true}))
	require.False(t, requiredMicrosoftMailFoldersCompleted(map[string]bool{"Junk": true}))
	require.True(t, requiredMicrosoftMailFoldersCompleted(map[string]bool{"Inbox": true, "Junk": true}))
}

func TestMicrosoftHistoryNormalizationOmitsUnusedRawPayloads(t *testing.T) {
	normalizedGraph := normalizeGraphFetchedMessage(graphMessage{ID: "message", Body: graphMessageBody{Content: "body"}}, defaultMicrosoftMailFolders[0], false)
	require.Equal(t, "body", normalizedGraph.Body)
	require.Empty(t, normalizedGraph.ProviderPayload)

	var imapMessage MicrosoftFetchedMessage
	applyIMAPBody(&imapMessage, []byte("Subject: Test\r\n\r\nbody"), false)
	require.Equal(t, "body", imapMessage.Body)
	require.Empty(t, imapMessage.RawSource)
}

func TestMicrosoftMailFetchClientGraphSessionFailureKeepsRefreshToken(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		newGraphSession: func(context.Context, string, int) (*msacl.Session, error) {
			return nil, errors.New("graph session unavailable")
		},
	}

	result, err := client.fetchGraphAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "authoritative-refresh-token",
		AccessToken:  "access-token",
	})

	require.Error(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "request", result.Category)
	require.Equal(t, "authoritative-refresh-token", result.RefreshToken)
}

func TestMicrosoftMailFetchClientTemporaryGraphIdentityFailureFallsBackToIMAP(t *testing.T) {
	client := &MicrosoftMailFetchClient{
		graphIdentity: func(context.Context, *msacl.Session, string, string) (microsoftGraphIdentityStatus, error) {
			return microsoftGraphIdentityUnavailable, errors.New("temporary profile failure")
		},
		exchangeIMAPToken: func(context.Context, MicrosoftMailFetchRequest) (string, string, MicrosoftMailFetchResult, error) {
			return "imap-access-token", "imap-rotated-rt", MicrosoftMailFetchResult{}, nil
		},
	}
	imapFallback := &fakeMicrosoftIMAPClient{result: MicrosoftMailFetchResult{
		Valid:        true,
		Protocol:     "imap",
		FolderCounts: map[string]int{},
	}}

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "configured@example.com",
		ClientID:     "client-id",
		RefreshToken: "refresh-token",
		AccessToken:  "access-token",
	}, imapFallback)

	require.NoError(t, err)
	require.True(t, result.Valid)
	require.True(t, imapFallback.called)
	require.Equal(t, "imap", result.Protocol)
}

func TestMicrosoftGraphIdentityStatusUsesMailAndUPN(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		profile  graphIdentityProfile
		status   microsoftGraphIdentityStatus
	}{
		{name: "mail", expected: "owner@example.com", profile: graphIdentityProfile{Mail: "OWNER@example.com"}, status: microsoftGraphIdentityMatched},
		{name: "upn", expected: "owner@example.com", profile: graphIdentityProfile{UserPrincipalName: "owner@example.com"}, status: microsoftGraphIdentityMatched},
		{name: "mismatch", expected: "owner@example.com", profile: graphIdentityProfile{Mail: "other@example.com"}, status: microsoftGraphIdentityMismatched},
		{name: "empty", expected: "owner@example.com", profile: graphIdentityProfile{}, status: microsoftGraphIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.status, microsoftGraphIdentityStatusFor(tt.expected, tt.profile))
		})
	}
}

func TestMicrosoftIMAPAuthenticationFailureClassification(t *testing.T) {
	require.True(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(&imap.Error{
		Type: imap.StatusResponseTypeNo,
		Code: imap.ResponseCodeAuthenticationFailed,
		Text: "AUTHENTICATE failed.",
	}))
	require.True(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(&imap.Error{
		Type: imap.StatusResponseTypeNo,
		Text: "AUTHENTICATE failed.",
	}))
	require.False(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(&imap.Error{
		Type: imap.StatusResponseTypeNo,
		Code: imap.ResponseCodeUnavailable,
		Text: "Service temporarily unavailable",
	}))
	require.False(t, isDefinitiveMicrosoftIMAPAuthenticationFailure(io.EOF))
}

func TestDialHTTPConnectHonorsContextWhenProxyGoesSilent(t *testing.T) {
	proxyAddress := startSilentMicrosoftMailProxy(t)
	parsed, err := url.Parse("http://" + proxyAddress)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	conn, err := dialHTTPConnect(ctx, &net.Dialer{Timeout: time.Second}, parsed, outlookIMAPAddress)
	if conn != nil {
		_ = conn.Close()
	}

	require.Error(t, err)
	require.Less(t, time.Since(started), 2*time.Second)
}

func TestDialOutlookIMAPSOCKSHandshakeHonorsContext(t *testing.T) {
	proxyAddress := startSilentMicrosoftMailProxy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	conn, err := dialOutlookIMAPConn(ctx, "socks5://"+proxyAddress)
	if conn != nil {
		_ = conn.Close()
	}

	require.Error(t, err)
	require.Less(t, time.Since(started), 2*time.Second)
}

func startSilentMicrosoftMailProxy(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		_ = listener.Close()
	})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buffer := make([]byte, 1024)
		_, _ = conn.Read(buffer)
		<-release
	}()
	return listener.Addr().String()
}

func TestMicrosoftMailFetchClientRejectsIncompleteCredentialsWithSpecificCategory(t *testing.T) {
	client := NewMicrosoftMailFetchClient()

	result, err := client.fetchAll(context.Background(), MicrosoftMailFetchRequest{
		EmailAddress: "user@example.com",
		ClientID:     "client-id",
	}, nil)

	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, "missing_token", result.Category)
	assert.Equal(t, "Microsoft mail fetch credentials are incomplete.", result.SafeMessage)
}

func TestClassifyMicrosoftGraphFailureKeepsAuthFailureGranularity(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		category    string
		safeMessage string
		proxy       bool
	}{
		{
			name: "unauthorized",
			err: &microsoftGraphHTTPError{
				statusCode: 401,
				message:    "invalid token",
			},
			category:    "graph_unauthorized",
			safeMessage: "Microsoft Graph access token is unauthorized or expired.",
		},
		{
			name: "forbidden",
			err: &microsoftGraphHTTPError{
				statusCode: 403,
				message:    "missing permission",
			},
			category:    "graph_forbidden",
			safeMessage: "Microsoft Graph mailbox permission is not available.",
		},
		{
			name: "rate limited",
			err: &microsoftGraphHTTPError{
				statusCode: 429,
				message:    "too many requests",
			},
			category:    "request",
			safeMessage: "Microsoft mail service is temporarily unavailable.",
			proxy:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category, message, proxy := classifyMicrosoftGraphFailure(tt.err)

			assert.Equal(t, tt.category, category)
			assert.Equal(t, tt.safeMessage, message)
			assert.Equal(t, tt.proxy, proxy)
		})
	}
}
