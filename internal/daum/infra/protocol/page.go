package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type imapPageCursor struct {
	Folders map[string]mailbox.IMAPPageCursor `json:"folders"`
}

func fetchIMAPPage(ctx context.Context, client *imapclient.Client, folders []mailFolder, request FetchRequest) (FetchResult, error) {
	cursor := imapPageCursor{Folders: make(map[string]mailbox.IMAPPageCursor)}
	if request.Page.Cursor != "" {
		if err := json.Unmarshal([]byte(request.Page.Cursor), &cursor); err != nil || cursor.Folders == nil {
			return FetchResult{}, &Failure{Category: "protocol", SafeMessage: "Daum mailbox scan cursor is invalid.", Retryable: true}
		}
	}
	result := FetchResult{Page: &mailbox.PageResult{Complete: true}, Complete: true}
	completed := make(map[string]bool)
	filter := request
	filter.KnownMessageIDs = nil
	for _, folder := range folders {
		if completed[folder.Label] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return FetchResult{}, transportFailure(err)
		}
		selected, err := client.Select(folder.ID, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			continue
		}
		before := cursor.Folders[folder.ID]
		uids, next, err := mailbox.IMAPUIDPage(ctx, client, selected, &before, request.SinceAt, request.UntilAt, request.Page.Limit)
		if err != nil {
			return FetchResult{}, transportFailure(err)
		}
		if len(uids) > 0 {
			body := &imap.FetchItemBodySection{Peek: true}
			command := client.Fetch(uids, &imap.FetchOptions{UID: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{body}})
			for data := command.Next(); data != nil; data = command.Next() {
				buffered, err := data.Collect()
				if err != nil {
					_ = command.Close()
					return FetchResult{}, transportFailure(err)
				}
				message, err := parseMessage(buffered.FindBodySection(body), fmt.Sprintf("imap:%s:%d:%d", folder.ID, selected.UIDValidity, buffered.UID), folder.ID, buffered.InternalDate)
				if err != nil {
					_ = command.Close()
					return FetchResult{}, err
				}
				if acceptsMessage(filter, message) {
					result.Messages = append(result.Messages, message)
				}
			}
			if err := command.Close(); err != nil {
				return FetchResult{}, transportFailure(err)
			}
		}
		cursor.Folders[folder.ID] = next
		result.Page.Complete = result.Page.Complete && next.Complete
		completed[folder.Label] = true
	}
	_ = client.Logout().Wait()
	if !completed["Inbox"] || !completed["Junk"] {
		return FetchResult{}, &Failure{Category: "incomplete_history", SafeMessage: "Inbox and Junk must both be read completely.", Retryable: true}
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return FetchResult{}, err
	}
	result.Page.NextCursor = string(raw)
	sort.SliceStable(result.Messages, func(i, j int) bool { return result.Messages[i].ReceivedAt.After(result.Messages[j].ReceivedAt) })
	return result, nil
}

type popPageCursor struct {
	BeforeUID string `json:"beforeUid"`
	Complete  bool   `json:"complete"`
}

func fetchPOP3Page(ctx context.Context, client *textproto.Conn, request FetchRequest) (FetchResult, error) {
	cursor := popPageCursor{}
	if request.Page.Cursor != "" {
		if err := json.Unmarshal([]byte(request.Page.Cursor), &cursor); err != nil {
			return FetchResult{}, &Failure{Category: "protocol", SafeMessage: "Daum mailbox scan cursor is invalid.", Retryable: true}
		}
	}
	if _, err := pop3Command(client, "UIDL"); err != nil {
		return FetchResult{}, transportFailure(err)
	}
	lines, err := readPOP3Lines(client)
	if err != nil {
		return FetchResult{}, transportFailure(err)
	}
	type entry struct {
		number int
		uid    string
	}
	entries := make([]entry, 0, len(lines))
	seen := make(map[string]bool)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return FetchResult{}, &Failure{Category: "protocol", SafeMessage: "Daum returned an invalid POP3 listing.", Retryable: true}
		}
		number, err := strconv.Atoi(fields[0])
		if err != nil || number < 1 || seen[fields[1]] {
			return FetchResult{}, &Failure{Category: "protocol", SafeMessage: "Daum returned an invalid POP3 listing.", Retryable: true}
		}
		seen[fields[1]] = true
		entries = append(entries, entry{number, fields[1]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].number > entries[j].number })
	start := 0
	if cursor.BeforeUID != "" {
		for index, item := range entries {
			if item.uid == cursor.BeforeUID {
				start = index + 1
				break
			}
		}
	}
	limit := min(max(request.Page.Limit, 1), 1000)
	end := min(start+limit, len(entries))
	result := FetchResult{Complete: true, Page: &mailbox.PageResult{Complete: end == len(entries)}}
	filter := request
	filter.KnownMessageIDs = nil
	for _, item := range entries[start:end] {
		if err := ctx.Err(); err != nil {
			return FetchResult{}, transportFailure(err)
		}
		if _, err := pop3Command(client, "RETR %d", item.number); err != nil {
			return FetchResult{}, transportFailure(err)
		}
		raw, err := io.ReadAll(client.DotReader())
		if err != nil {
			return FetchResult{}, transportFailure(err)
		}
		message, err := parseMessage(raw, "pop3:"+item.uid, "INBOX", time.Time{})
		if err != nil {
			return FetchResult{}, err
		}
		if acceptsMessage(filter, message) {
			result.Messages = append(result.Messages, message)
		}
		cursor.BeforeUID = item.uid
	}
	cursor.Complete = result.Page.Complete
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return FetchResult{}, err
	}
	result.Page.NextCursor = string(encoded)
	_, _ = pop3Command(client, "QUIT")
	sort.SliceStable(result.Messages, func(i, j int) bool { return result.Messages[i].ReceivedAt.After(result.Messages[j].ReceivedAt) })
	return result, nil
}
