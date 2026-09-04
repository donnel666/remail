package infra

import (
	"context"
	"fmt"
	"time"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

// AdminViewRepo runs the platform-wide (unscoped) aggregates for the admin
// dashboard. Read-only raw SQL across orders, code-receipt delivery heads,
// users and the resource inventory tables. Finance and per-project inventory
// are supplied by ports (billing/alloc), not here.
type AdminViewRepo struct {
	db *gorm.DB
}

func NewAdminViewRepo(db *gorm.DB) *AdminViewRepo { return &AdminViewRepo{db: db} }

var _ dashboardapp.AdminView = (*AdminViewRepo)(nil)

const (
	adminOrderProductType       = "COALESCE(NULLIF(product_type, 'random'), allocation_type)"
	adminJoinedOrderProductType = "COALESCE(NULLIF(o.product_type, 'random'), o.allocation_type)"
)

func (r *AdminViewRepo) OrderTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]dashboardapp.CountBucket, error) {
	// sqlFormat is a fixed internal constant (see app.sqlFormat), never user input.
	sel := fmt.Sprintf("DATE_FORMAT(created_at, '%s') AS bucket, COUNT(*) AS count", sqlFormat)
	var rows []dashboardapp.CountBucket
	if err := r.db.WithContext(ctx).
		Table("orders").
		Select(sel).
		Where("debit_tx_id IS NOT NULL AND created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).
		Where(historyOrderExclude).
		Group("bucket").
		Order("bucket ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *AdminViewRepo) CodeOrderTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]dashboardapp.TypeCountBucket, error) {
	sel := fmt.Sprintf("DATE_FORMAT(created_at, '%s') AS bucket, %s AS product_type, COUNT(*) AS count", sqlFormat, adminOrderProductType)
	var rows []dashboardapp.TypeCountBucket
	if err := r.db.WithContext(ctx).
		Table("orders").
		Select(sel).
		Where("service_mode = 'code' AND debit_tx_id IS NOT NULL AND created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).
		Where(historyOrderExclude).
		Group("bucket, " + adminOrderProductType).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *AdminViewRepo) CodeReceiptTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]dashboardapp.TypeReceiptBucket, error) {
	// Anchored to the order's created_at (like the console) so receipts stay a
	// subset of code orders in the same bucket; split by the delivered resource type.
	sel := fmt.Sprintf(
		"DATE_FORMAT(o.created_at, '%s') AS bucket, %s AS product_type, COUNT(*) AS received, COALESCE(ROUND(AVG(GREATEST(TIMESTAMPDIFF(SECOND, o.receive_started_at, h.message_received_at),0))),0) AS avg_seconds, COALESCE(SUM(CASE WHEN o.receive_started_at IS NOT NULL THEN GREATEST(TIMESTAMPDIFF(SECOND, o.receive_started_at, h.message_received_at),0) ELSE 0 END),0) AS total_seconds, COUNT(o.receive_started_at) AS timed",
		sqlFormat, adminJoinedOrderProductType,
	)
	var rows []dashboardapp.TypeReceiptBucket
	if err := r.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Joins("JOIN orders AS o ON o.id = h.order_id").
		Select(sel).
		Where("o.service_mode = 'code' AND o.debit_tx_id IS NOT NULL AND o.created_at >= ? AND o.created_at <= ?", from.UTC(), to.UTC()).
		Where("o." + historyOrderExclude).
		Group("bucket, " + adminJoinedOrderProductType).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *AdminViewRepo) PurchaseSummaries(ctx context.Context, from, to time.Time) ([]dashboardapp.TypePurchaseSummary, error) {
	var summaries []dashboardapp.TypePurchaseSummary
	err := r.db.WithContext(ctx).
		Table("orders").
		Select(adminOrderProductType+" AS product_type, COUNT(*) AS orders, COUNT(activated_at) AS activated, COALESCE(SUM(CASE WHEN activated_at IS NOT NULL AND receive_started_at IS NOT NULL THEN GREATEST(TIMESTAMPDIFF(SECOND, receive_started_at, activated_at),0) ELSE 0 END),0) AS total_seconds, COUNT(CASE WHEN activated_at IS NOT NULL AND receive_started_at IS NOT NULL THEN 1 END) AS timed").
		Where("service_mode = 'purchase' AND debit_tx_id IS NOT NULL AND created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).
		Where(historyOrderExclude).
		Group(adminOrderProductType).
		Scan(&summaries).Error
	return summaries, err
}

func (r *AdminViewRepo) NewUserTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]dashboardapp.CountBucket, error) {
	sel := fmt.Sprintf("DATE_FORMAT(created_at, '%s') AS bucket, COUNT(*) AS count", sqlFormat)
	var rows []dashboardapp.CountBucket
	if err := r.db.WithContext(ctx).
		Table("users").
		Select(sel).
		Where("created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).
		Group("bucket").
		Order("bucket ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ActiveUserTrend counts each non-deleted user once, in the bucket of their
// latest login or API-key use that falls inside the selected range.
func (r *AdminViewRepo) ActiveUserTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]dashboardapp.CountBucket, error) {
	sel := fmt.Sprintf("DATE_FORMAT(activity.last_active_at, '%s') AS bucket, COUNT(*) AS count", sqlFormat)
	var rows []dashboardapp.CountBucket
	if err := r.db.WithContext(ctx).Raw(`
SELECT `+sel+`
FROM (
    SELECT
        u.id,
        GREATEST(
            COALESCE(CASE WHEN u.last_login_at >= ? AND u.last_login_at <= ? THEN u.last_login_at END, '1970-01-01 00:00:00'),
            COALESCE(MAX(CASE WHEN ak.last_used_at >= ? AND ak.last_used_at <= ? THEN ak.last_used_at END), '1970-01-01 00:00:00')
        ) AS last_active_at
    FROM users u
    LEFT JOIN api_keys ak ON ak.user_id = u.id
    WHERE u.status <> 'deleted'
    GROUP BY u.id, u.last_login_at
) activity
WHERE activity.last_active_at <> '1970-01-01 00:00:00'
GROUP BY bucket
ORDER BY bucket ASC`, from.UTC(), to.UTC(), from.UTC(), to.UTC()).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *AdminViewRepo) TotalUsers(ctx context.Context) (int, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Table("users").
		Where("status <> ?", "deleted").
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// InventorySnapshot is a point-in-time count (there is no historical snapshot
// table, so the trend flat-lines these). "Available" mirrors what the platform
// treats as sellable/usable.
func (r *AdminViewRepo) InventorySnapshot(ctx context.Context) (dashboardapp.InventorySnapshot, error) {
	var snap dashboardapp.InventorySnapshot
	counts := []struct {
		out   *int
		table string
		where string
	}{
		{&snap.MicrosoftTotal, "microsoft_resources", "status <> 'deleted'"},
		{&snap.MicrosoftAvailable, "microsoft_resources", "status = 'normal' AND for_sale = TRUE AND graph_available = TRUE"},
		{&snap.DomainTotal, "generated_mailboxes", "status <> 'retired'"},
		{&snap.DomainAvailable, "generated_mailboxes", "status = 'normal'"},
		{&snap.GmailTotal, "gmail_resources", "status <> 'deleted'"},
		{&snap.ICloudTotal, "icloud_resources", "status <> 'deleted'"},
	}
	for _, c := range counts {
		var n int64
		if err := r.db.WithContext(ctx).Table(c.table).Where(c.where).Count(&n).Error; err != nil {
			return dashboardapp.InventorySnapshot{}, err
		}
		*c.out = int(n)
	}
	var gmailAvailable int64
	if err := r.db.WithContext(ctx).
		Table("gmail_resources AS gr").
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = 'gmail'").
		Joins("JOIN users AS owner ON owner.id = er.owner_user_id").
		Where("gr.status IN ('normal', 'available') AND gr.for_sale = TRUE").
		Where("owner.status = 'active' AND owner.role IN ('supplier', 'admin', 'super_admin')").
		Count(&gmailAvailable).Error; err != nil {
		return dashboardapp.InventorySnapshot{}, err
	}
	snap.GmailAvailable = int(gmailAvailable)
	domains := runtimeconfig.ICloudForwardingSuffixes(runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""))
	if len(domains) == 0 {
		return snap, nil
	}
	var iCloudAvailable int64
	if err := r.db.WithContext(ctx).
		Table("icloud_resources AS ir").
		Where("ir.status = 'normal' AND ir.for_sale = TRUE").
		Where(`EXISTS (
			SELECT 1 FROM icloud_aliases AS ia
			WHERE ia.resource_id = ir.id
			  AND ia.status = 'normal'
			  AND LOWER(SUBSTR(ia.forward_to_email, INSTR(ia.forward_to_email, '@') + 1)) IN ?
			  AND EXISTS (
				  SELECT 1 FROM domain_resources AS forwarding_domain
				  WHERE forwarding_domain.purpose = 'binding'
				    AND forwarding_domain.status NOT IN ('disabled', 'deleted')
				    AND LOWER(forwarding_domain.domain) = LOWER(SUBSTR(ia.forward_to_email, INSTR(ia.forward_to_email, '@') + 1))
			  )
			  AND NOT EXISTS (
				  SELECT 1 FROM icloud_allocations AS active
				  WHERE active.alias_id = ia.id AND active.status = 'allocated'
			  )
		)`, domains).
		Count(&iCloudAvailable).Error; err != nil {
		return dashboardapp.InventorySnapshot{}, err
	}
	snap.ICloudAvailable = int(iCloudAvailable)
	return snap, nil
}

func (r *AdminViewRepo) ProjectCodeRanking(ctx context.Context, from, to time.Time, limit int) ([]dashboardapp.ProjectCountRow, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows []struct {
		ProjectID uint   `gorm:"column:project_id"`
		Name      string `gorm:"column:name"`
		Count     int    `gorm:"column:count"`
	}
	if err := r.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Joins("JOIN orders AS o ON o.id = h.order_id").
		Joins("LEFT JOIN projects AS p ON p.id = o.project_id").
		Select("o.project_id AS project_id, COALESCE(p.name, '') AS name, COUNT(*) AS count").
		Where("o.service_mode = 'code' AND o.debit_tx_id IS NOT NULL AND o.created_at >= ? AND o.created_at <= ?", from.UTC(), to.UTC()).
		Where("o." + historyOrderExclude).
		Group("o.project_id, name").
		Order("count DESC, o.project_id ASC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]dashboardapp.ProjectCountRow, len(rows))
	for i := range rows {
		out[i] = dashboardapp.ProjectCountRow(rows[i])
	}
	return out, nil
}
