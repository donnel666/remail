package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
)

type codeDiagnosisOrderRow struct {
	OrderNo                  string
	ProjectID                uint
	ProjectName              string
	ServiceMode              string
	Status                   string
	EmailResourceID          uint
	DeliveryStoredAt         *time.Time
	ResourceAbnormalRefunded bool
}

func (r *Repo) LookupCodeDiagnosis(ctx context.Context, userID uint, email string) (app.CodeDiagnosisLookup, error) {
	var rows []codeDiagnosisOrderRow
	if err := r.dbFor(ctx).Raw(`
SELECT
    o.order_no,
	o.project_id,
	project.name AS project_name,
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
		)
    ) AS resource_abnormal_refunded
FROM orders o
JOIN projects project ON project.id = o.project_id
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
  AND o.order_no NOT LIKE 'HIST-%'
ORDER BY
    CASE WHEN o.status IN ('pending_payment', 'paid', 'active') THEN 0 ELSE 1 END ASC,
    o.created_at DESC,
    o.id DESC
LIMIT 2`, userID, email).Scan(&rows).Error; err != nil {
		return app.CodeDiagnosisLookup{}, fmt.Errorf("lookup code diagnosis orders: %w", err)
	}
	lookup := app.CodeDiagnosisLookup{Orders: make([]app.CodeDiagnosisOrderFact, len(rows))}
	for i, row := range rows {
		lookup.Orders[i] = app.CodeDiagnosisOrderFact{
			OrderNo: row.OrderNo, ProjectID: row.ProjectID, ProjectName: row.ProjectName,
			ServiceMode: row.ServiceMode, Status: row.Status,
			EmailResourceID: row.EmailResourceID, DeliveryStoredAt: row.DeliveryStoredAt,
			ResourceAbnormalRefunded: row.ResourceAbnormalRefunded,
		}
	}
	return lookup, nil
}
