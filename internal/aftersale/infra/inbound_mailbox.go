package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	"gorm.io/gorm"
)

type inboundMailboxRow struct {
	ID              uint
	Recipient       string
	EnvelopeFrom    string
	SourceObjectKey string
}

type InboundMailbox struct {
	db        *gorm.DB
	files     aftersaleapp.FileStorePort
	localPart string
	domain    string
}

func NewInboundMailbox(db *gorm.DB, files aftersaleapp.FileStorePort, config aftersaleapp.TicketMailConfig) *InboundMailbox {
	return &InboundMailbox{
		db:        db,
		files:     files,
		localPart: strings.ToLower(strings.TrimSpace(config.ReplyLocalPart)),
		domain:    strings.ToLower(strings.TrimSpace(config.ReplyDomain)),
	}
}

func (m *InboundMailbox) ListTicketReplies(ctx context.Context, ticketNo string, since time.Time, limit int) ([]aftersaleapp.InboundMailboxMessage, error) {
	ticketNo = strings.ToLower(strings.TrimSpace(ticketNo))
	if m == nil || m.db == nil || m.files == nil || m.localPart == "" || m.domain == "" || ticketNo == "" {
		return nil, fmt.Errorf("list aftersale inbound replies: mailbox unavailable")
	}
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 100)
	pattern := escapeLikePattern(m.localPart+"+"+ticketNo+"-") + "%@" + escapeLikePattern(m.domain)
	var rows []inboundMailboxRow
	query := m.db.WithContext(ctx).
		Table("inbound_mails AS im").
		Select("im.id, im.recipient, im.envelope_from, im.source_object_key").
		Where("im.resource_type = ? AND im.status = ?", "domain", "stored").
		Where("im.recipient LIKE ?", pattern).
		Where("NOT EXISTS (SELECT 1 FROM aftersale_inbound_receipts AS receipt WHERE receipt.inbound_mail_id = im.id)").
		Order("im.created_at ASC, im.id ASC").
		Limit(limit)
	if !since.IsZero() {
		query = query.Where("im.created_at >= ?", since.UTC())
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list aftersale inbound replies: %w", err)
	}
	messages := make([]aftersaleapp.InboundMailboxMessage, 0, len(rows))
	for _, row := range rows {
		_, raw, err := m.files.Read(ctx, row.SourceObjectKey)
		if err != nil {
			return nil, fmt.Errorf("read aftersale inbound reply %d: %w", row.ID, err)
		}
		messages = append(messages, aftersaleapp.InboundMailboxMessage{
			ID:           row.ID,
			Recipient:    row.Recipient,
			EnvelopeFrom: row.EnvelopeFrom,
			Raw:          raw,
		})
	}
	return messages, nil
}

var _ aftersaleapp.InboundMailboxPort = (*InboundMailbox)(nil)
