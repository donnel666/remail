package infra

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type microsoftGraphPageCursor struct {
	NextLink string `json:"nextLink,omitempty"`
	Complete bool   `json:"complete"`
}

type microsoftMailPageCursor struct {
	Protocol string                              `json:"protocol"`
	Graph    map[string]microsoftGraphPageCursor `json:"graph,omitempty"`
	IMAP     map[string]mailbox.IMAPPageCursor   `json:"imap,omitempty"`
}

func readMicrosoftMailPageCursor(raw, protocol string) microsoftMailPageCursor {
	var cursor microsoftMailPageCursor
	if json.Unmarshal([]byte(raw), &cursor) != nil {
		cursor = microsoftMailPageCursor{}
	}
	cursor.Protocol = protocol
	if cursor.Graph == nil {
		cursor.Graph = make(map[string]microsoftGraphPageCursor)
	}
	if cursor.IMAP == nil {
		cursor.IMAP = make(map[string]mailbox.IMAPPageCursor)
	}
	return cursor
}

func microsoftMailPageResult(cursor microsoftMailPageCursor, complete bool) *mailbox.PageResult {
	encoded, _ := json.Marshal(cursor)
	return &mailbox.PageResult{NextCursor: string(encoded), Complete: complete}
}

func validMicrosoftGraphPageURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, "graph.microsoft.com") &&
		u.User == nil && u.Fragment == "" && strings.HasPrefix(u.Path, "/v1.0/")
}

func fetchMicrosoftGraphPage(
	ctx context.Context,
	session *msacl.Session,
	accessToken string,
	req MicrosoftMailFetchRequest,
	result MicrosoftMailFetchResult,
	fetchFolder func(context.Context, *msacl.Session, string, MicrosoftMailFolder, MicrosoftMailFetchRequest) (microsoftFolderFetchResult, error),
) (MicrosoftMailFetchResult, error) {
	cursor := readMicrosoftMailPageCursor(req.Page.Cursor, "graph")
	complete := true
	for _, folder := range defaultMicrosoftMailFolders {
		previous := cursor.Graph[folder.ID]
		if previous.Complete {
			continue
		}
		folderRequest := req
		folderRequest.Page = &mailbox.PageRequest{Cursor: previous.NextLink, Limit: min(max(req.Page.Limit, 1), 1000)}
		folderRequest.OnMessages = nil
		folderResult, err := fetchFolder(ctx, session, accessToken, folder, folderRequest)
		if err != nil {
			category, message, proxyFailure := classifyMicrosoftGraphFailure(err)
			failure := microsoftMailFetchFailure(category, message, proxyFailure)
			failure.RefreshToken = result.RefreshToken
			return failure, err
		}
		next := microsoftGraphPageCursor{NextLink: folderResult.NextLink, Complete: folderResult.NextLink == ""}
		cursor.Graph[folder.ID] = next
		complete = complete && next.Complete
		result.FolderCounts[folder.Label] = folderResult.Count
		result.Messages = append(result.Messages, folderResult.Messages...)
	}
	result.Messages = newestMicrosoftMessages(result.Messages, 0)
	result.MessageCount = len(result.Messages)
	result.Page = microsoftMailPageResult(cursor, complete)
	return result, nil
}

func fetchMicrosoftIMAPPage(
	ctx context.Context,
	client *imapclient.Client,
	folders []MicrosoftMailFolder,
	req MicrosoftMailFetchRequest,
	result MicrosoftMailFetchResult,
) (MicrosoftMailFetchResult, error) {
	cursor := readMicrosoftMailPageCursor(req.Page.Cursor, "imap")
	completedFolders := make(map[string]bool)
	complete := true
	for _, folder := range folders {
		if completedFolders[folder.Label] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return microsoftMailFetchFailure("request", "Microsoft mail service is temporarily unavailable.", false), err
		}
		selected, err := client.Select(folder.ID, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			continue
		}
		previous := cursor.IMAP[folder.ID]
		uids, next, err := mailbox.IMAPUIDPage(ctx, client, selected, &previous, req.SinceAt, req.UntilAt, req.Page.Limit)
		if err != nil {
			return microsoftMailFetchFailure("request", "Microsoft mailbox page could not be read completely.", false), err
		}
		if len(uids) > 0 {
			section := &imap.FetchItemBodySection{Peek: true}
			rows, err := client.Fetch(uids, &imap.FetchOptions{
				UID: true, Envelope: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section},
			}).Collect()
			if err != nil {
				return microsoftMailFetchFailure("request", "Microsoft mailbox page could not be read completely.", false), err
			}
			for _, row := range rows {
				message := normalizeIMAPFetchedMessage(row, folder, section, true)
				if inMicrosoftFetchWindow(message.ReceivedAt, req.SinceAt, req.UntilAt) {
					result.Messages = append(result.Messages, message)
					result.FolderCounts[folder.Label]++
				}
			}
		}
		cursor.IMAP[folder.ID] = next
		complete = complete && next.Complete
		completedFolders[folder.Label] = true
	}
	if !requiredMicrosoftMailFoldersCompleted(completedFolders) {
		return microsoftMailFetchFailure("request", "Inbox and Junk must both be read completely.", false), nil
	}
	result.Messages = newestMicrosoftMessages(result.Messages, 0)
	result.MessageCount = len(result.Messages)
	result.Page = microsoftMailPageResult(cursor, complete)
	return result, nil
}
