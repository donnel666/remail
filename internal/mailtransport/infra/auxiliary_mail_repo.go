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

// ResourceExists is an explicitly bounded read-only scope check: it validates
// visibility without importing or mutating a Core GORM model.
func (r *AuxiliaryMailRepo) ResourceExists(ctx context.Context, resourceID uint, resourceType domain.InboundResourceType) (bool, error) {
	if resourceID == 0 {
		return false, nil
	}
	var count int64
	query := r.dbFor(ctx).
		Table("email_resources AS er").
		Where("er.id = ?", resourceID)
	switch resourceType {
	case domain.InboundResourceDomain:
		query = query.Joins("JOIN domain_resources AS dr ON dr.id = er.id AND er.type = 'domain' AND dr.purpose = 'binding'")
	case domain.InboundResourceMicrosoft:
		query = query.Joins("JOIN microsoft_resources AS mr ON mr.id = er.id AND er.type = 'microsoft'")
	case domain.InboundResourceICloud:
		query = query.Joins("JOIN icloud_resources AS ir ON ir.id = er.id AND er.type = 'icloud' AND COALESCE(NULLIF(ir.required_forward_to, ''), ir.selected_forward_to) <> ''")
	default:
		return false, nil
	}
	err := query.Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check resource for auxiliary mail: %w", err)
	}
	return count > 0, nil
}

func (r *AuxiliaryMailRepo) ListMessages(ctx context.Context, filter mailapp.AuxiliaryMailFilter) ([]domain.InboundMail, int64, bool, error) {
	if filter.ResourceType == "" {
		filter.ResourceType = domain.InboundResourceMicrosoft
	}
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
			"(COALESCE(im.received_at, im.created_at) < ? OR (COALESCE(im.received_at, im.created_at) = ? AND im.id < ?))",
			filter.BeforeReceivedAt.UTC(), filter.BeforeReceivedAt.UTC(), filter.BeforeID,
		)
	} else {
		pageQuery = pageQuery.Offset(filter.Offset)
	}
	var models []InboundMailModel
	if err := pageQuery.
		Select("im.id, im.header_from, im.recipient, im.subject, im.body_preview, im.verification_code, im.received_at, im.status, im.created_at").
		Order("COALESCE(im.received_at, im.created_at) DESC, im.id DESC").
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
		items[i].SourceObjectKey = ""
		items[i].EnvelopeFrom = ""
	}
	return items, total, hasMore, nil
}

func (r *AuxiliaryMailRepo) auxiliaryListQuery(ctx context.Context, filter mailapp.AuxiliaryMailFilter, table string) *gorm.DB {
	query := r.dbFor(ctx).Table(table + " AS im")
	if filter.ResourceType == domain.InboundResourceDomain {
		// ponytail: recipient-domain scan is enough for an admin detail tab; add an indexed recipient_domain only if this query becomes slow.
		query = query.
			Joins("JOIN domain_resources AS dr ON dr.id = ? AND dr.purpose = 'binding'", filter.ResourceID).
			Where(`(
(im.resource_type = ? AND im.resource_id = dr.id)
OR (im.resource_type = ? AND LOWER(SUBSTRING_INDEX(im.recipient, '@', -1)) = LOWER(dr.domain))
)`, string(domain.InboundResourceDomain), string(domain.InboundResourceMicrosoft))
	} else if filter.ResourceType == domain.InboundResourceICloud {
		query = query.
			Joins("JOIN icloud_resources AS ir ON ir.id = ? AND COALESCE(NULLIF(ir.required_forward_to, ''), ir.selected_forward_to) <> ''", filter.ResourceID).
			Where("LOWER(im.recipient) = LOWER(COALESCE(NULLIF(ir.required_forward_to, ''), ir.selected_forward_to))")
	} else {
		query = query.Where("im.resource_id = ? AND im.resource_type = ?", filter.ResourceID, string(domain.InboundResourceMicrosoft))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		pattern := "%" + strings.ToLower(escapeAuxiliaryLike(search)) + "%"
		query = query.Where(`(
LOWER(im.recipient) LIKE ? ESCAPE '\\'
OR LOWER(im.header_from) LIKE ? ESCAPE '\\'
OR LOWER(im.subject) LIKE ? ESCAPE '\\'
OR LOWER(im.body_preview) LIKE ? ESCAPE '\\'
OR LOWER(im.verification_code) LIKE ? ESCAPE '\\'
)`, pattern, pattern, pattern, pattern, pattern)
	}
	return query
}

func (r *AuxiliaryMailRepo) FindMessage(ctx context.Context, resourceID uint, resourceType domain.InboundResourceType, messageID uint) (*domain.InboundMail, error) {
	if resourceID == 0 || messageID == 0 {
		return nil, nil
	}
	if resourceType == "" {
		resourceType = domain.InboundResourceMicrosoft
	}
	var model InboundMailModel
	query := r.dbFor(ctx).
		Table("inbound_mails AS im").
		Select(`im.id, im.header_from, im.recipient, im.subject, im.body_preview,
im.verification_code, im.message_id_header, im.received_at, im.parsed_at,
im.resource_id, im.resource_type, im.source_object_key, im.status,
im.failure_reason, im.created_at, im.updated_at`).
		Joins("JOIN email_resources AS er ON er.id = im.resource_id AND er.type = im.resource_type").
		Where("im.id = ?", messageID)
	switch resourceType {
	case domain.InboundResourceDomain:
		query = query.
			Joins("JOIN domain_resources AS dr ON dr.id = ? AND dr.purpose = 'binding'", resourceID).
			Where(`(
(im.resource_type = ? AND im.resource_id = dr.id)
OR (im.resource_type = ? AND LOWER(SUBSTRING_INDEX(im.recipient, '@', -1)) = LOWER(dr.domain))
)`, string(domain.InboundResourceDomain), string(domain.InboundResourceMicrosoft))
	case domain.InboundResourceMicrosoft:
		query = query.
			Joins("JOIN microsoft_resources AS mr ON mr.id = er.id").
			Where("im.resource_id = ? AND im.resource_type = ?", resourceID, string(domain.InboundResourceMicrosoft))
	case domain.InboundResourceICloud:
		query = query.
			Joins("JOIN icloud_resources AS ir ON ir.id = ? AND COALESCE(NULLIF(ir.required_forward_to, ''), ir.selected_forward_to) <> ''", resourceID).
			Where("LOWER(im.recipient) = LOWER(COALESCE(NULLIF(ir.required_forward_to, ''), ir.selected_forward_to))")
	default:
		return nil, nil
	}
	err := query.Take(&model).Error
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
