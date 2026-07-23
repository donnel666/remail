package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"gorm.io/gorm"
)

// AdminAllocationEnrichmentRepo performs an explicitly bounded cross-context
// read composition. It only receives the order numbers from one already-paged
// Alloc result and returns safe display facts; it never writes Trade, Core, IAM
// or MailMatch state and never reads mail bodies, tokens or wallet ledgers.
type AdminAllocationEnrichmentRepo struct {
	db *gorm.DB
}

func NewAdminAllocationEnrichmentRepo(db *gorm.DB) *AdminAllocationEnrichmentRepo {
	return &AdminAllocationEnrichmentRepo{db: db}
}

type adminAllocationEnrichmentRow struct {
	OrderNo          string     `gorm:"column:order_no"`
	ProjectName      string     `gorm:"column:project_name"`
	ProjectLogoURL   string     `gorm:"column:project_logo_url"`
	DeliveryEmail    string     `gorm:"column:delivery_email"`
	ServiceMode      string     `gorm:"column:service_mode"`
	OrderStatus      string     `gorm:"column:order_status"`
	PayAmount        string     `gorm:"column:pay_amount"`
	BuyerEmail       string     `gorm:"column:buyer_email"`
	VerificationCode string     `gorm:"column:verification_code"`
	ReceiveUntil     *time.Time `gorm:"column:receive_until"`
}

func (r *AdminAllocationEnrichmentRepo) GetAdminAllocationEnrichments(ctx context.Context, orderNos []string) (map[string]allocapp.AdminAllocationEnrichment, error) {
	result := make(map[string]allocapp.AdminAllocationEnrichment)
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("administrator allocation enrichment repository is unavailable")
	}
	orderNos = uniqueAdminAllocationOrderNos(orderNos)
	if len(orderNos) == 0 {
		return result, nil
	}
	if len(orderNos) > 100 {
		return nil, fmt.Errorf("administrator allocation enrichment page is too large")
	}

	var rows []adminAllocationEnrichmentRow
	if err := r.db.WithContext(ctx).
		Table("orders AS o").
		Select(`
o.order_no AS order_no,
p.name AS project_name,
p.logo_url AS project_logo_url,
o.delivery_email AS delivery_email,
o.service_mode AS service_mode,
o.status AS order_status,
CAST(o.pay_amount AS CHAR) AS pay_amount,
u.email AS buyer_email,
COALESCE(CASE
    WHEN (CASE WHEN mp.message_id IS NULL THEN m.matched_order_id ELSE mp.matched_order_id END) = o.id
    THEN (CASE WHEN mp.message_id IS NULL THEN m.verification_code ELSE mp.verification_code END)
    ELSE ''
END, '') AS verification_code,
o.receive_until AS receive_until`).
		Joins("JOIN projects AS p ON p.id = o.project_id").
		Joins("JOIN users AS u ON u.id = o.user_id").
		Joins("LEFT JOIN mailmatch_order_delivery_heads AS h ON h.order_id = o.id").
		Joins("LEFT JOIN mailmatch_messages AS m ON m.id = h.message_id").
		Joins("LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id").
		Where("o.order_no IN ?", orderNos).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query administrator allocation enrichments: %w", err)
	}
	for _, row := range rows {
		var logoURL *string
		if value := strings.TrimSpace(row.ProjectLogoURL); value != "" {
			logoURL = &value
		}
		var verificationCode *string
		if value := strings.TrimSpace(row.VerificationCode); value != "" {
			verificationCode = &value
		}
		result[row.OrderNo] = allocapp.AdminAllocationEnrichment{
			OrderNo: row.OrderNo, ProjectName: row.ProjectName, ProjectLogoURL: logoURL,
			DeliveryEmail: row.DeliveryEmail, ServiceMode: row.ServiceMode, OrderStatus: row.OrderStatus,
			PayAmount: row.PayAmount, BuyerEmail: row.BuyerEmail, VerificationCode: verificationCode,
			ReceiveUntil: row.ReceiveUntil,
		}
	}
	return result, nil
}

func uniqueAdminAllocationOrderNos(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
