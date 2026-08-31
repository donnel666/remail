package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
)

type codeDiagnosisOrderRow struct {
	OrderNo                  string
	ServiceMode              string
	Status                   string
	EmailResourceID          uint
	DeliveryStoredAt         *time.Time
	ResourceAbnormalRefunded bool
}

func (r *Repo) LookupCodeDiagnosis(ctx context.Context, userID uint, email string, projectID uint) (app.CodeDiagnosisLookup, error) {
	var rows []codeDiagnosisOrderRow
	if err := r.dbFor(ctx).Raw(`
SELECT
    o.order_no,
    o.service_mode,
    o.status,
    COALESCE(ma.resource_id, da.resource_id, ga.resource_id, ia.resource_id, 0) AS email_resource_id,
	message.created_at AS delivery_stored_at,
    (
        o.status IN ('refunded', 'failed')
        AND o.refund_tx_id IS NOT NULL
		AND EXISTS (
			SELECT 1
			FROM order_events refund_event
			WHERE refund_event.order_no = o.order_no
			  AND refund_event.event_type = 'order.refunded'
			  AND refund_event.operator_type = 'system'
			  AND refund_event.reason IN (
				  'Microsoft resource is permanently unavailable.',
				  '自有 Gmail 资源不可用，订单已退款。',
				  '自有 Gmail 凭据失效，订单已退款。'
			  )
		)
    ) AS resource_abnormal_refunded
FROM orders o
LEFT JOIN microsoft_allocations ma
    ON ma.order_no = o.order_no AND o.allocation_type = 'microsoft'
LEFT JOIN domain_allocations da
    ON da.order_no = o.order_no AND o.allocation_type = 'domain'
LEFT JOIN gmail_allocations ga
    ON ga.order_no = o.order_no AND o.allocation_type = 'gmail'
LEFT JOIN icloud_allocations ia
    ON ia.order_no = o.order_no AND o.allocation_type = 'icloud'
LEFT JOIN mailmatch_order_delivery_heads h ON h.order_id = o.id
LEFT JOIN mailmatch_messages message ON message.id = h.message_id
WHERE o.user_id = ?
  AND o.delivery_email = ?
  AND o.project_id = ?
  AND o.order_no NOT LIKE 'HIST-%'
ORDER BY
    CASE WHEN o.status IN ('pending_payment', 'paid', 'active') THEN 0 ELSE 1 END ASC,
    o.created_at DESC,
    o.id DESC
LIMIT 2`, userID, email, projectID).Scan(&rows).Error; err != nil {
		return app.CodeDiagnosisLookup{}, fmt.Errorf("lookup code diagnosis orders: %w", err)
	}
	lookup := app.CodeDiagnosisLookup{Orders: make([]app.CodeDiagnosisOrderFact, len(rows))}
	for i, row := range rows {
		lookup.Orders[i] = app.CodeDiagnosisOrderFact{
			OrderNo: row.OrderNo, ServiceMode: row.ServiceMode, Status: row.Status,
			EmailResourceID: row.EmailResourceID, DeliveryStoredAt: row.DeliveryStoredAt,
			ResourceAbnormalRefunded: row.ResourceAbnormalRefunded,
		}
	}
	if len(rows) > 0 {
		lookup.EmailOrderExists = true
		return lookup, nil
	}
	var count int64
	if err := r.dbFor(ctx).Table("orders").
		Where("user_id = ? AND delivery_email = ? AND order_no NOT LIKE 'HIST-%'", userID, email).
		Limit(1).
		Count(&count).Error; err != nil {
		return app.CodeDiagnosisLookup{}, fmt.Errorf("check code diagnosis email orders: %w", err)
	}
	lookup.EmailOrderExists = count > 0
	return lookup, nil
}
