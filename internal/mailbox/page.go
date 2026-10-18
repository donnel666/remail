package mailbox

import (
	"context"
	"sort"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type PageRequest struct {
	Cursor string
	Limit  int
}

type PageResult struct {
	NextCursor string
	Complete   bool
}

type IMAPPageCursor struct {
	UIDValidity uint32   `json:"uidValidity"`
	BeforeUID   imap.UID `json:"beforeUid"`
	Complete    bool     `json:"complete"`
}

// IMAPUIDPage advances by stable UID, never by a shrinking sequence number.
// Its cursor is committed by the caller only after every returned fact is stored.
func IMAPUIDPage(ctx context.Context, client *imapclient.Client, selected *imap.SelectData, cursor *IMAPPageCursor, since, until time.Time, limit int) (imap.UIDSet, IMAPPageCursor, error) {
	next := IMAPPageCursor{UIDValidity: selected.UIDValidity}
	if cursor != nil && cursor.UIDValidity == selected.UIDValidity {
		next = *cursor
	}
	if next.Complete || selected.NumMessages == 0 {
		next.Complete = true
		return nil, next, ctx.Err()
	}
	criteria := &imap.SearchCriteria{}
	if !since.IsZero() {
		criteria.Since = since.UTC().Add(-24 * time.Hour)
	}
	if !until.IsZero() {
		criteria.Before = until.UTC().Add(48 * time.Hour)
	}
	if next.BeforeUID > 0 {
		if next.BeforeUID == 1 {
			next.Complete = true
			return nil, next, ctx.Err()
		}
		var before imap.UIDSet
		before.AddRange(1, next.BeforeUID-1)
		criteria.UID = []imap.UIDSet{before}
	}
	found, err := client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, next, err
	}
	uids := found.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
	limit = min(max(limit, 1), 1000)
	next.Complete = len(uids) <= limit
	if len(uids) > limit {
		uids = uids[:limit]
	}
	if len(uids) > 0 {
		next.BeforeUID = uids[len(uids)-1]
	}
	return imap.UIDSetNum(uids...), next, ctx.Err()
}
