package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"gorm.io/gorm"
)

const adminMessageMailboxSQL = `CASE
    WHEN m.recipient = mr.email_address THEN 'main'
    WHEN EXISTS (
        SELECT 1 FROM explicit_aliases ea
        WHERE ea.resource_id = m.email_resource_id AND ea.email = m.recipient
    ) THEN 'alias'
    WHEN EXISTS (
        SELECT 1 FROM dot_aliases da
        WHERE da.resource_id = m.email_resource_id AND da.email = m.recipient
    ) THEN 'dot'
    WHEN EXISTS (
        SELECT 1 FROM plus_aliases pa
        WHERE pa.resource_id = m.email_resource_id AND pa.email = m.recipient
    ) THEN 'plus'
    ELSE 'main'
END`

type adminMessageRow struct {
	ID               uint
	Mailbox          string
	Recipient        string
	Sender           string
	Subject          string
	Preview          string
	Status           string
	VerificationCode string
	OrderNo          sql.NullString
	ReceivedAt       sql.NullTime
	Body             sql.NullString
	MatchDiagnostic  string
}

type AdminMessageRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

func NewAdminMessageRepo(db *gorm.DB) *AdminMessageRepo {
	return &AdminMessageRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
}

func (r *AdminMessageRepo) AdminMessageResourceExists(ctx context.Context, resourceID uint, resourceType domain.ResourceType) (bool, error) {
	var found uint
	query := r.db.WithContext(ctx).
		Table("email_resources AS er").
		Select("er.id").
		Where("er.id = ? AND er.type = ?", resourceID, string(resourceType))
	if resourceType == domain.ResourceTypeDomain {
		query = query.Joins("JOIN domain_resources dr ON dr.id = er.id")
	} else {
		query = query.Joins("JOIN microsoft_resources mr ON mr.id = er.id")
	}
	err := query.
		Limit(1).
		Scan(&found).Error
	if err != nil {
		return false, fmt.Errorf("find admin message resource: %w", err)
	}
	return found != 0, nil
}

func (r *AdminMessageRepo) ListAdminMessageSummaries(
	ctx context.Context,
	query app.AdminMessageListQuery,
) ([]app.AdminMessageSummary, int64, error) {
	base := r.adminMessageBaseQuery(ctx, query.ResourceID, query.ResourceType, query.Search)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count admin message summaries: %w", err)
	}
	var rows []adminMessageRow
	selectSQL := strings.Join([]string{
		"m.id",
		adminMessageMailboxSelect(query.ResourceType) + " AS mailbox",
		"m.recipient",
		"m.sender",
		"m.subject",
		"m.body_preview AS preview",
		"m.status",
		"m.verification_code",
		"o.order_no",
		"m.received_at",
	}, ", ")
	if err := base.Session(&gorm.Session{}).
		Select(selectSQL).
		Order("m.received_at DESC, m.id DESC").
		Offset(query.Offset).
		Limit(query.Limit).
		Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list admin message summaries: %w", err)
	}
	items := make([]app.AdminMessageSummary, len(rows))
	for i := range rows {
		items[i] = adminMessageSummaryFromRow(rows[i])
	}
	return items, total, nil
}

func (r *AdminMessageRepo) FindAdminMessageDetailWithLog(
	ctx context.Context,
	resourceID uint,
	resourceType domain.ResourceType,
	messageID uint,
	log *governancedomain.OperationLog,
) (*app.AdminMessageDetail, error) {
	var detail *app.AdminMessageDetail
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row adminMessageRow
		selectSQL := strings.Join([]string{
			"m.id",
			adminMessageMailboxSelect(resourceType) + " AS mailbox",
			"m.recipient",
			"m.sender",
			"m.subject",
			"m.body_preview AS preview",
			"m.status",
			"m.verification_code",
			"o.order_no",
			"m.received_at",
			"m.raw_body AS body",
			"m.match_diagnostic",
		}, ", ")
		query := adminMessageBaseQueryDB(tx.WithContext(ctx), resourceID, resourceType, "")
		err := query.
			Select(selectSQL).
			Where("m.id = ?", messageID).
			Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrMessageNotFound
		}
		if err != nil {
			return fmt.Errorf("find admin message detail: %w", err)
		}
		summary := adminMessageSummaryFromRow(row)
		detail = &app.AdminMessageDetail{
			AdminMessageSummary: summary,
			Body:                row.Body.String,
			MatchDiagnostic:     safeAdminMatchDiagnostic(row.MatchDiagnostic),
		}
		if log != nil {
			if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func (r *AdminMessageRepo) adminMessageBaseQuery(ctx context.Context, resourceID uint, resourceType domain.ResourceType, search string) *gorm.DB {
	return adminMessageBaseQueryDB(r.db.WithContext(ctx), resourceID, resourceType, search)
}

func adminMessageBaseQueryDB(db *gorm.DB, resourceID uint, resourceType domain.ResourceType, search string) *gorm.DB {
	db = db.
		Table("mailmatch_messages AS m").
		Joins("LEFT JOIN orders o ON o.id = m.matched_order_id").
		Where("m.email_resource_id = ? AND m.resource_type = ?", resourceID, string(resourceType))
	if resourceType == domain.ResourceTypeDomain {
		db = db.Joins("JOIN domain_resources dr ON dr.id = m.email_resource_id")
	} else {
		db = db.Joins("JOIN microsoft_resources mr ON mr.id = m.email_resource_id")
	}
	search = strings.TrimSpace(search)
	if search == "" {
		return db
	}
	like := "%" + search + "%"
	return db.Where(`
        m.recipient LIKE ?
        OR m.sender LIKE ?
        OR m.subject LIKE ?
        OR m.body_preview LIKE ?
        OR m.raw_body LIKE ?
        OR m.verification_code LIKE ?`, like, like, like, like, like, like)
}

func adminMessageMailboxSelect(resourceType domain.ResourceType) string {
	if resourceType == domain.ResourceTypeDomain {
		return "'main'"
	}
	return adminMessageMailboxSQL
}

func adminMessageSummaryFromRow(row adminMessageRow) app.AdminMessageSummary {
	var verificationCode *string
	if value := strings.TrimSpace(row.VerificationCode); value != "" {
		verificationCode = &value
	}
	var orderNo *string
	if row.OrderNo.Valid {
		value := strings.TrimSpace(row.OrderNo.String)
		if value != "" {
			orderNo = &value
		}
	}
	return app.AdminMessageSummary{
		ID:               row.ID,
		Mailbox:          normalizeAdminMailbox(row.Mailbox),
		Recipient:        strings.TrimSpace(row.Recipient),
		Sender:           strings.TrimSpace(row.Sender),
		Subject:          strings.TrimSpace(row.Subject),
		Preview:          truncateUTF8Bytes(row.Preview, 1000),
		Status:           domain.MessageStatus(row.Status),
		VerificationCode: verificationCode,
		OrderNo:          orderNo,
		ReceivedAt:       row.ReceivedAt.Time.UTC(),
	}
}

func normalizeAdminMailbox(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "alias", "dot", "plus":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "main"
	}
}

func safeAdminMatchDiagnostic(value string) *string {
	value = strings.TrimSpace(value)
	switch value {
	case "Message did not match any active order service.", "Message matched multiple active order services.":
		return &value
	case "":
		return nil
	default:
		safe := "Message matching diagnostic is unavailable."
		return &safe
	}
}
