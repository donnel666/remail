package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
)

type AuxiliaryMailRepo struct {
	db *gorm.DB
}

func NewAuxiliaryMailRepo(db *gorm.DB) *AuxiliaryMailRepo {
	return &AuxiliaryMailRepo{db: db}
}

// MicrosoftResourceExists is an explicitly bounded read-only scope check:
// it validates visibility without importing or mutating a Core GORM model.
func (r *AuxiliaryMailRepo) MicrosoftResourceExists(ctx context.Context, resourceID uint) (bool, error) {
	if resourceID == 0 {
		return false, nil
	}
	var count int64
	err := r.dbFor(ctx).
		Table("email_resources AS er").
		Joins("JOIN microsoft_resources AS mr ON mr.id = er.id AND er.type = 'microsoft'").
		Where("er.id = ?", resourceID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check microsoft resource for auxiliary mail: %w", err)
	}
	return count > 0, nil
}

func (r *AuxiliaryMailRepo) ListByMicrosoftResource(ctx context.Context, filter mailapp.AuxiliaryMailFilter) ([]domain.InboundMail, int64, bool, error) {
	total := int64(-1)
	if !filter.SkipTotal {
		countQuery := r.auxiliaryListQuery(ctx, filter, "inbound_mails")
		if err := countQuery.Count(&total).Error; err != nil {
			return nil, 0, false, fmt.Errorf("count auxiliary messages: %w", err)
		}
	}

	pageQuery := r.auxiliaryListQuery(ctx, filter, "inbound_mails")
	if filter.BeforeReceivedAt != nil {
		pageQuery = pageQuery.Where(
			"(COALESCE(received_at, created_at) < ? OR (COALESCE(received_at, created_at) = ? AND id < ?))",
			filter.BeforeReceivedAt.UTC(), filter.BeforeReceivedAt.UTC(), filter.BeforeID,
		)
	} else {
		pageQuery = pageQuery.Offset(filter.Offset)
	}
	var models []InboundMailModel
	if err := pageQuery.
		Select("id, header_from, recipient, subject, body_preview, verification_code, received_at, status, created_at").
		Order("COALESCE(received_at, created_at) DESC, id DESC").
		Limit(filter.Limit + 1).
		Find(&models).Error; err != nil {
		return nil, 0, false, fmt.Errorf("list auxiliary messages: %w", err)
	}
	hasMore := len(models) > filter.Limit
	if hasMore {
		models = models[:filter.Limit]
	}
	items := make([]domain.InboundMail, len(models))
	for i := range models {
		items[i] = *models[i].toDomain()
		// Q11 is a DB-only safe summary. Keeping this field empty makes an
		// accidental object-store read in the application layer impossible.
		items[i].SourceObjectKey = ""
		items[i].EnvelopeFrom = ""
	}
	return items, total, hasMore, nil
}

func (r *AuxiliaryMailRepo) auxiliaryListQuery(ctx context.Context, filter mailapp.AuxiliaryMailFilter, table string) *gorm.DB {
	query := r.dbFor(ctx).
		Table(table).
		Where("resource_id = ? AND resource_type = ?", filter.ResourceID, string(domain.InboundResourceMicrosoft))
	if search := strings.TrimSpace(filter.Search); search != "" {
		pattern := "%" + strings.ToLower(escapeAuxiliaryLike(search)) + "%"
		query = query.Where(`(
LOWER(recipient) LIKE ? ESCAPE '\\'
OR LOWER(header_from) LIKE ? ESCAPE '\\'
OR LOWER(subject) LIKE ? ESCAPE '\\'
OR LOWER(body_preview) LIKE ? ESCAPE '\\'
OR LOWER(verification_code) LIKE ? ESCAPE '\\'
)`, pattern, pattern, pattern, pattern, pattern)
	}
	return query
}

func (r *AuxiliaryMailRepo) FindByMicrosoftResource(ctx context.Context, resourceID, messageID uint) (*domain.InboundMail, error) {
	if resourceID == 0 || messageID == 0 {
		return nil, nil
	}
	var model InboundMailModel
	err := r.dbFor(ctx).
		Table("inbound_mails AS im").
		Select(`im.id, im.header_from, im.recipient, im.subject, im.body_preview,
im.verification_code, im.message_id_header, im.received_at, im.parsed_at,
im.resource_id, im.resource_type, im.source_object_key, im.status,
im.failure_reason, im.created_at, im.updated_at`).
		Joins("JOIN email_resources AS er ON er.id = im.resource_id AND er.type = im.resource_type").
		Joins("JOIN microsoft_resources AS mr ON mr.id = er.id").
		Where("im.id = ? AND im.resource_id = ? AND im.resource_type = ?", messageID, resourceID, string(domain.InboundResourceMicrosoft)).
		Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find auxiliary message by resource: %w", err)
	}
	return model.toDomain(), nil
}

func (r *AuxiliaryMailRepo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func escapeAuxiliaryLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
