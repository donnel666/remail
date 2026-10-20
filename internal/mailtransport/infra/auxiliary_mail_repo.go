package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dauminfra "github.com/donnel666/remail/internal/daum/infra"
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
	case domain.InboundResourceDaum:
		query = query.Joins("JOIN daum_resources dr ON dr.id = er.id AND er.type = 'daum' AND dr.status <> 'deleted'")
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
		countQuery, err := r.auxiliaryListQuery(ctx, filter, "inbound_mails")
		if err != nil {
			return nil, 0, false, err
		}
		if err := countQuery.Count(&total).Error; err != nil {
			return nil, 0, false, fmt.Errorf("count auxiliary messages: %w", err)
		}
	}

	pageQuery, err := r.auxiliaryListQuery(ctx, filter, "inbound_mails")
	if err != nil {
		return nil, 0, false, err
	}
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

func (r *AuxiliaryMailRepo) auxiliaryListQuery(ctx context.Context, filter mailapp.AuxiliaryMailFilter, table string) (*gorm.DB, error) {
	query := r.dbFor(ctx).Table(table + " AS im")
	switch filter.ResourceType {
	case domain.InboundResourceDaum:
		var err error
		query, err = r.daumAuxiliaryMessages(query, filter.ResourceID)
		if err != nil {
			return nil, err
		}
	case domain.InboundResourceDomain:
		// ponytail: recipient-domain scan is enough for an admin detail tab; add an indexed recipient_domain only if this query becomes slow.
		query = query.
			Joins("JOIN domain_resources AS dr ON dr.id = ? AND dr.purpose = 'binding'", filter.ResourceID).
			Where(`(
(im.resource_type = ? AND im.resource_id = dr.id)
OR (im.resource_type = ? AND LOWER(SUBSTRING_INDEX(im.recipient, '@', -1)) = LOWER(dr.domain))
)`, string(domain.InboundResourceDomain), string(domain.InboundResourceMicrosoft))
	case domain.InboundResourceICloud:
		query = query.
			Joins("JOIN icloud_resources AS ir ON ir.id = ? AND COALESCE(NULLIF(ir.required_forward_to, ''), ir.selected_forward_to) <> ''", filter.ResourceID).
			Where("LOWER(im.recipient) = LOWER(COALESCE(NULLIF(ir.required_forward_to, ''), ir.selected_forward_to))")
	default:
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
	return query, nil
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
	case domain.InboundResourceDaum:
		var err error
		query, err = r.daumAuxiliaryMessages(query, resourceID)
		if err != nil {
			return nil, err
		}
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

// Only an owned domain without a pickup capability may read raw inbound mail.
func (r *AuxiliaryMailRepo) daumAuxiliaryMessages(query *gorm.DB, resourceID uint) (*gorm.DB, error) {
	return query.Joins("JOIN daum_binding_mailboxes dbm ON dbm.resource_id=? AND dbm.status='linked' AND dbm.pickup_url='' AND dbm.order_no=''", resourceID).
		Joins("JOIN daum_resources dr ON dr.id=dbm.resource_id AND dr.owner_user_id=dbm.owner_user_id AND dr.status<>'deleted'").
		Joins("JOIN email_resources daum_root ON daum_root.id=dr.id AND daum_root.type='daum' AND daum_root.owner_user_id=dbm.owner_user_id").
		Joins("JOIN domain_resources binding_domain ON binding_domain.id=dbm.domain_resource_id AND binding_domain.status='normal' AND binding_domain.owner_user_id=dbm.owner_user_id").
		Joins("JOIN email_resources domain_root ON domain_root.id=binding_domain.id AND domain_root.type='domain' AND domain_root.owner_user_id=dbm.owner_user_id").
		Where("im.resource_type='domain' AND im.resource_id=binding_domain.id AND LOWER(im.recipient)=LOWER(dbm.binding_address)"), nil
}

// A URL association never falls back to raw inbox access, even after revocation.
func (r *AuxiliaryMailRepo) DaumPickupBinding(ctx context.Context, id uint) (*mailapp.AuxiliaryPickupBinding, bool, error) {
	var row struct {
		ID, OwnerUserID                                            uint
		BindingAddress, PickupURL, OrderNo, Status, AllocationType string
		DomainResourceID, MailboxResourceID                        *uint
	}
	err := r.dbFor(ctx).Table("daum_binding_mailboxes dbm").Select("dbm.id,dbm.owner_user_id,dbm.binding_address,dbm.pickup_url,dbm.order_no,dbm.status,dbm.domain_resource_id,dbm.mailbox_resource_id,source_order.allocation_type").Joins("LEFT JOIN orders source_order ON source_order.order_no=dbm.order_no").Where("dbm.resource_id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if row.PickupURL == "" {
		return nil, false, nil
	}
	email, token, err := dauminfra.ParsePickupURL(row.PickupURL)
	if err != nil || row.Status != "linked" || row.OwnerUserID == 0 || row.OrderNo == "" || !strings.EqualFold(email, row.BindingAddress) {
		return nil, true, nil
	}
	resourceID := row.MailboxResourceID
	if resourceID == nil {
		resourceID = row.DomainResourceID
	}
	if resourceID == nil || *resourceID == 0 {
		return nil, true, nil
	}
	table, ok := map[string]string{"microsoft": "microsoft_resources", "microsoft_enterprise": "microsoft_enterprise_resources", "domain": "domain_resources", "gmail": "gmail_resources", "icloud": "icloud_resources", "proto": "proto_resources", "daum": "daum_resources"}[row.AllocationType]
	if !ok {
		return nil, true, nil
	}
	db := r.dbFor(ctx).Session(&gorm.Session{Logger: platform.NewGormLogger()})
	query := db.Table("daum_binding_mailboxes dbm").
		Joins("JOIN daum_resources dr ON dr.id=dbm.resource_id AND dr.owner_user_id=dbm.owner_user_id AND dr.status<>'deleted'").
		Joins("JOIN email_resources root ON root.id=dr.id AND root.type='daum' AND root.owner_user_id=dbm.owner_user_id").
		Joins("JOIN orders source_order ON source_order.order_no=dbm.order_no AND source_order.user_id=dbm.owner_user_id AND LOWER(source_order.delivery_email)=LOWER(dbm.binding_address)").
		Joins("JOIN order_tokens token ON token.order_no=source_order.order_no AND token.enabled=TRUE AND token.token_plain=?", token).
		Joins("JOIN email_resources source_root ON source_root.id=? AND source_root.type=source_order.allocation_type", *resourceID).
		Joins("JOIN "+table+" source ON source.id=source_root.id AND source.status<>'deleted'").
		Joins("JOIN "+strings.TrimSuffix(table, "_resources")+"_allocations allocation ON allocation.order_no=source_order.order_no AND allocation.resource_id=source_root.id AND LOWER(allocation.email)=LOWER(dbm.binding_address) AND allocation.status='allocated'").
		Where("dbm.id=? AND dbm.resource_id=? AND dbm.owner_user_id=? AND dbm.pickup_url=? AND dbm.order_no=? AND LOWER(dbm.binding_address)=? AND COALESCE(dbm.mailbox_resource_id,dbm.domain_resource_id)=? AND dbm.status='linked' AND source_order.allocation_type=?", row.ID, id, row.OwnerUserID, row.PickupURL, row.OrderNo, email, *resourceID, row.AllocationType).
		Where("(token.expire_at IS NULL OR token.expire_at>?) AND source_order.status IN ('active','completed') AND (source_order.service_mode='purchase' OR (source_order.service_mode='code' AND (source_order.receive_until IS NULL OR source_order.receive_until>?)))", time.Now().UTC(), time.Now().UTC())
	if row.AllocationType == "gmail" {
		query = query.Where("allocation.source='local'")
	}
	if row.AllocationType == "domain" {
		_, suffix, _ := strings.Cut(email, "@")
		query = query.Where("source.status='normal' AND LOWER(source.domain)=? AND source.owner_user_id=source_root.owner_user_id", suffix)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return nil, true, err
	}
	if count == 0 {
		return nil, true, nil
	}
	return &mailapp.AuxiliaryPickupBinding{OwnerUserID: row.OwnerUserID, ResourceID: *resourceID, OrderNo: row.OrderNo, Email: email, Token: token}, true, nil
}

func (r *AuxiliaryMailRepo) DaumBinding(ctx context.Context, resourceID uint) (*mailapp.AuxiliaryBindingSummary, error) {
	var result mailapp.AuxiliaryBindingSummary
	err := r.dbFor(ctx).Table("daum_binding_mailboxes").Select("id, binding_address AS email_address, status, updated_at").Where("resource_id = ?", resourceID).Take(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Daum auxiliary binding: %w", err)
	}
	return &result, nil
}
