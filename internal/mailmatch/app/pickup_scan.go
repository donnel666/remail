package app

import (
	"context"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailmatch/domain"
)

// Body caches are short-lived; scan progress survives them and advances only
// after the entire bounded page has been ingested under the account lease.
type PickupScanProgress struct {
	ResourceType       domain.ResourceType `json:"resourceType"`
	CredentialRevision uint64              `json:"credentialRevision"`
	SinceAt            time.Time           `json:"sinceAt"`
	BackfillUntil      time.Time           `json:"backfillUntil"`
	BackfillCursor     string              `json:"backfillCursor"`
	BackfillComplete   bool                `json:"backfillComplete"`
	NextBackfill       bool                `json:"nextBackfill"`
}

type PickupScanStatePort interface {
	LoadScanProgress(context.Context, uint) (*PickupScanProgress, error)
	StoreScanProgress(context.Context, uint, string, PickupScanProgress, time.Duration) (bool, error)
}

type pickupScanPage struct {
	progress PickupScanProgress
	backfill bool
	request  mailbox.PageRequest
	since    time.Time
	until    time.Time
}

func nextPickupScanPage(scope OrderScope, since, until time.Time, previous *PickupScanProgress) pickupScanPage {
	progress := PickupScanProgress{ResourceType: scope.AllocationType, CredentialRevision: scope.CredentialRevision,
		SinceAt: since, BackfillUntil: until, NextBackfill: true}
	if previous != nil && previous.ResourceType == scope.AllocationType && previous.CredentialRevision == scope.CredentialRevision {
		progress = *previous
		if since.Before(progress.SinceAt) {
			progress.SinceAt, progress.BackfillUntil, progress.BackfillCursor = since, until, ""
			progress.BackfillComplete, progress.NextBackfill = false, true
		}
	}
	page := pickupScanPage{progress: progress, backfill: progress.NextBackfill, request: mailbox.PageRequest{Limit: boundedRuntimeInt("mail_stream_batch_size", 100, 1000)}}
	if page.backfill {
		if progress.BackfillComplete {
			page.progress.SinceAt, page.progress.BackfillUntil, page.progress.BackfillCursor = since, until, ""
			page.progress.BackfillComplete = false
		}
		page.since, page.until, page.request.Cursor = page.progress.SinceAt, page.progress.BackfillUntil, page.progress.BackfillCursor
	} else {
		// A new UID may carry an old Date or arrive in a different folder.
		// Fresh heads therefore cover the whole current order window, not a Date watermark.
		page.since, page.until = since, until
	}
	return page
}

func (page pickupScanPage) completed(result *mailbox.PageResult) (PickupScanProgress, error) {
	if result == nil || (!result.Complete && (result.NextCursor == "" || result.NextCursor == page.request.Cursor)) {
		return page.progress, domain.ErrMailServiceUnavailable
	}
	progress := page.progress
	if page.backfill {
		progress.BackfillCursor, progress.BackfillComplete, progress.NextBackfill = result.NextCursor, result.Complete, false
	} else {
		progress.NextBackfill = true
	}
	return progress, nil
}
