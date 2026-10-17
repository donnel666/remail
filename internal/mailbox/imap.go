package mailbox

import (
	"context"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"time"
)

func RecentIMAPSeqSet(ctx context.Context, client *imapclient.Client, total uint32, sinceAt time.Time, maxMessages int) (imap.SeqSet, error) {
	if total == 0 {
		return nil, nil
	}
	criteria := &imap.SearchCriteria{}
	if !sinceAt.IsZero() {
		criteria.Since = sinceAt.UTC()
	}
	if maxMessages > 0 && client.Caps().Has(imap.CapSort) {
		nums, err := client.Sort(&imapclient.SortOptions{
			SearchCriteria: criteria,
			SortCriteria:   []imapclient.SortCriterion{{Key: imapclient.SortKeyArrival, Reverse: true}},
		}).Wait()
		if err == nil {
			return imap.SeqSetNum(nums[:min(len(nums), maxMessages)]...), ctx.Err()
		}
	}
	if maxMessages > 0 {
		return LatestIMAPSeqSet(total, maxMessages), ctx.Err()
	}

	seqSet := imap.SeqSet{}
	if sinceAt.IsZero() {
		seqSet.AddRange(1, total)
	} else {
		data, err := client.Search(criteria, nil).Wait()
		if err != nil {
			return nil, err
		}
		seqSet = imap.SeqSetNum(data.AllSeqNums()...)
	}
	return seqSet, ctx.Err()
}

func LatestIMAPSeqSet(total uint32, maxMessages int) imap.SeqSet {
	if total == 0 || maxMessages <= 0 {
		return nil
	}
	start := uint32(1)
	if total > uint32(maxMessages) {
		start = total - uint32(maxMessages) + 1
	}
	seqSet := imap.SeqSet{}
	seqSet.AddRange(start, total)
	return seqSet
}
