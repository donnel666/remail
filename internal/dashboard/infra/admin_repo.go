package infra

import (
	"context"
	"fmt"
	"time"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
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
	sel := fmt.Sprintf("DATE_FORMAT(created_at, '%s') AS bucket, product_type, COUNT(*) AS count", sqlFormat)
	var rows []dashboardapp.TypeCountBucket
	if err := r.db.WithContext(ctx).
		Table("orders").
		Select(sel).
		Where("service_mode = 'code' AND debit_tx_id IS NOT NULL AND created_at >= ? AND created_at <= ?", from.UTC(), to.UTC()).
		Group("bucket, product_type").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *AdminViewRepo) CodeReceiptTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]dashboardapp.TypeReceiptBucket, error) {
	// Anchored to the order's created_at (like the console) so receipts stay a
	// subset of code orders in the same bucket; split by the order's product_type.
	sel := fmt.Sprintf(
		"DATE_FORMAT(o.created_at, '%s') AS bucket, o.product_type, COUNT(*) AS received, COALESCE(GREATEST(ROUND(AVG(TIMESTAMPDIFF(SECOND, o.receive_started_at, h.message_received_at))),0),0) AS avg_seconds",
		sqlFormat,
	)
	var rows []dashboardapp.TypeReceiptBucket
	if err := r.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Joins("JOIN orders AS o ON o.id = h.order_id").
		Select(sel).
		Where("o.service_mode = 'code' AND o.created_at >= ? AND o.created_at <= ?", from.UTC(), to.UTC()).
		Group("bucket, o.product_type").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
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
// treats as sellable/usable: Microsoft = normal+for_sale+graph_available,
// domain mailbox = normal.
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
	}
	for _, c := range counts {
		var n int64
		if err := r.db.WithContext(ctx).Table(c.table).Where(c.where).Count(&n).Error; err != nil {
			return dashboardapp.InventorySnapshot{}, err
		}
		*c.out = int(n)
	}
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
		Where("o.service_mode = 'code' AND o.created_at >= ? AND o.created_at <= ?", from.UTC(), to.UTC()).
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
