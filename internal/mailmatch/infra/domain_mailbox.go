package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
)

type domainMailboxRow struct {
	ID              uint
	EnvelopeFrom    string
	Recipient       string
	SourceObjectKey string
	CreatedAt       time.Time
}

func (r *Repo) ListDomainMailboxMessages(
	ctx context.Context,
	scope app.OrderScope,
	since time.Time,
	until time.Time,
	limit int,
) ([]app.FetchedMessage, error) {
	mailboxKey := mailbox.Normalize(scope.Recipient)
	if scope.AllocationType != domain.ResourceTypeDomain || scope.EmailResourceID == 0 || mailboxKey == "" || limit <= 0 {
		return nil, domain.ErrInvalidRequest
	}
	if until.Before(since) {
		return nil, nil
	}
	var rows []domainMailboxRow
	if err := r.dbFor(ctx).
		Table("inbound_mails").
		Select("id, envelope_from, recipient, source_object_key, created_at").
		Where("resource_type = ? AND resource_id = ? AND mailbox_key = ?", string(domain.ResourceTypeDomain), scope.EmailResourceID, mailboxKey).
		Where("status = ?", "stored").
		Where("created_at >= ? AND created_at <= ?", since.UTC(), until.UTC()).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list domain mailbox messages: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if r.files == nil {
		return nil, fmt.Errorf("read domain mailbox messages: file store unavailable")
	}
	messages := make([]app.FetchedMessage, 0, len(rows))
	for _, row := range rows {
		stored, err := r.files.ReadPrivate(ctx, row.SourceObjectKey)
		if err != nil || stored == nil {
			if err == nil {
				err = fmt.Errorf("empty inbound object")
			}
			return nil, fmt.Errorf("read domain mailbox message %d: %w", row.ID, err)
		}
		messages = append(messages, app.ParseInboundFetchedMessage(app.InboundMailRequest{
			EmailResourceID:   scope.EmailResourceID,
			ResourceType:      domain.ResourceTypeDomain,
			Recipient:         row.Recipient,
			EnvelopeFrom:      row.EnvelopeFrom,
			Raw:               stored.ContentBytes,
			ReceivedAt:        row.CreatedAt.UTC(),
			ProviderMessageID: fmt.Sprintf("inbound:%d", row.ID),
			Protocol:          "smtp",
			Folder:            "inbound",
		}))
	}
	return messages, nil
}
