// Package infra implements the console dashboard read port with raw aggregate
// queries. It reads across orders, code-receipt delivery heads, wallets,
// projects and users — all in the one MySQL database — the same way billing's
// finance read model joins into tables it does not own. Read-only, no models.
package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type ViewRepo struct {
	db    *gorm.DB
	redis redis.UniversalClient
}

const (
	leaderboardCacheKey      = "dashboard:leaderboards:v1"
	leaderboardCacheTTL      = 15 * time.Minute
	successfulOrderPredicate = "((o.service_mode = 'code' AND h.order_id IS NOT NULL) OR (o.service_mode = 'purchase' AND o.activated_at IS NOT NULL))"
)

// ponytail: one JSON value keeps the two rankings atomic; use ZSETs only if the
// ranked-user count makes this snapshot materially large.
type leaderboardSnapshot struct {
	TodayStart time.Time
	Today      []dashboardapp.LeaderRow
	Historical []dashboardapp.LeaderRow
}

func NewViewRepo(db *gorm.DB, redisClient redis.UniversalClient) *ViewRepo {
	return &ViewRepo{db: db, redis: redisClient}
}

var _ dashboardapp.ConsoleView = (*ViewRepo)(nil)

func (r *ViewRepo) WalletSummary(ctx context.Context, userID uint) (balance, totalSpend float64, err error) {
	var row struct {
		ConsumerBalance string `gorm:"column:consumer_balance"`
		TotalSpend      string `gorm:"column:total_spend"`
	}
	if err := r.db.WithContext(ctx).
		Table("wallets").
		Select("consumer_balance, total_spend").
		Where("user_id = ?", userID).
		Scan(&row).Error; err != nil {
		return 0, 0, err
	}
	// Missing wallet row leaves the strings empty -> zeros.
	return parseMoney(row.ConsumerBalance), parseMoney(row.TotalSpend), nil
}

func (r *ViewRepo) OrderBuckets(ctx context.Context, userID uint, sqlFormat string, from, to time.Time) ([]dashboardapp.OrderBucketRow, error) {
	// sqlFormat is a fixed internal constant (see app.sqlFormat), never user input.
	sel := fmt.Sprintf(
		"DATE_FORMAT(CONVERT_TZ(created_at, '+00:00', '+08:00'), '%s') AS bucket, COUNT(*) AS orders, COALESCE(SUM(service_mode = 'code'),0) AS code_orders, COALESCE(SUM(pay_amount - refund_amount),0) AS spend",
		sqlFormat,
	)
	var rows []struct {
		Bucket     string `gorm:"column:bucket"`
		Orders     int    `gorm:"column:orders"`
		CodeOrders int    `gorm:"column:code_orders"`
		Spend      string `gorm:"column:spend"`
	}
	if err := r.db.WithContext(ctx).
		Table("orders").
		Select(sel).
		// debit_tx_id IS NOT NULL keeps only orders that were actually charged, so
		// never-paid (pending_payment) rows don't inflate spend or counts. Refunds
		// net out via pay_amount - refund_amount.
		Where("user_id = ? AND debit_tx_id IS NOT NULL AND created_at >= ? AND created_at <= ?", userID, from.UTC(), to.UTC()).
		Group("bucket").
		Order("bucket ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]dashboardapp.OrderBucketRow, len(rows))
	for i := range rows {
		out[i] = dashboardapp.OrderBucketRow{
			Bucket:     rows[i].Bucket,
			Orders:     rows[i].Orders,
			CodeOrders: rows[i].CodeOrders,
			Spend:      parseMoney(rows[i].Spend),
		}
	}
	return out, nil
}

func (r *ViewRepo) ReceiptBuckets(ctx context.Context, userID uint, sqlFormat string, from, to time.Time) ([]dashboardapp.ReceiptBucketRow, error) {
	// Bucketed by the ORDER's created_at (not the receipt time) so received counts
	// stay a subset of code orders in the same bucket — the per-bucket code
	// success rate the panels compute (receivedCodes/codeOrders) is then always
	// well-defined and <= 100%. Latency still uses the receipt timestamp.
	sel := fmt.Sprintf(
		"DATE_FORMAT(CONVERT_TZ(o.created_at, '+00:00', '+08:00'), '%s') AS bucket, COUNT(*) AS received, COALESCE(GREATEST(ROUND(AVG(TIMESTAMPDIFF(SECOND, o.receive_started_at, h.message_received_at))),0),0) AS avg_seconds",
		sqlFormat,
	)
	var rows []struct {
		Bucket     string `gorm:"column:bucket"`
		Received   int    `gorm:"column:received"`
		AvgSeconds int    `gorm:"column:avg_seconds"`
	}
	if err := r.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Joins("JOIN orders AS o ON o.id = h.order_id").
		Select(sel).
		Where("o.user_id = ? AND o.service_mode = 'code' AND o.created_at >= ? AND o.created_at <= ?", userID, from.UTC(), to.UTC()).
		Group("bucket").
		Order("bucket ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]dashboardapp.ReceiptBucketRow, len(rows))
	for i := range rows {
		out[i] = dashboardapp.ReceiptBucketRow(rows[i])
	}
	return out, nil
}

func (r *ViewRepo) ProjectCodeRanking(ctx context.Context, userID uint, from, to time.Time) ([]dashboardapp.ProjectCountRow, error) {
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
		Where("o.user_id = ? AND o.service_mode = 'code' AND o.created_at >= ? AND o.created_at <= ?", userID, from.UTC(), to.UTC()).
		Group("o.project_id, name").
		Order("count DESC, o.project_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]dashboardapp.ProjectCountRow, len(rows))
	for i := range rows {
		out[i] = dashboardapp.ProjectCountRow(rows[i])
	}
	return out, nil
}

func (r *ViewRepo) ProjectSpendBuckets(ctx context.Context, userID uint, projectIDs []uint, sqlFormat string, from, to time.Time) ([]dashboardapp.ProjectSpendRow, error) {
	if len(projectIDs) == 0 {
		return nil, nil
	}
	sel := fmt.Sprintf(
		"o.project_id AS project_id, DATE_FORMAT(CONVERT_TZ(o.created_at, '+00:00', '+08:00'), '%s') AS bucket, COALESCE(SUM(o.pay_amount - o.refund_amount),0) AS spend",
		sqlFormat,
	)
	var rows []struct {
		ProjectID uint   `gorm:"column:project_id"`
		Bucket    string `gorm:"column:bucket"`
		Spend     string `gorm:"column:spend"`
	}
	if err := r.db.WithContext(ctx).
		Table("orders AS o").
		Select(sel).
		Where("o.user_id = ? AND o.debit_tx_id IS NOT NULL AND o.project_id IN ? AND o.created_at >= ? AND o.created_at <= ?", userID, projectIDs, from.UTC(), to.UTC()).
		Group("o.project_id, bucket").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]dashboardapp.ProjectSpendRow, len(rows))
	for i := range rows {
		out[i] = dashboardapp.ProjectSpendRow{
			ProjectID: rows[i].ProjectID,
			Bucket:    rows[i].Bucket,
			Spend:     parseMoney(rows[i].Spend),
		}
	}
	return out, nil
}

func (r *ViewRepo) TodayCounts(ctx context.Context, userID uint, since time.Time) (orders, receipts int, err error) {
	var orderCount int64
	if err := r.db.WithContext(ctx).
		Table("orders").
		Where("user_id = ? AND debit_tx_id IS NOT NULL AND created_at >= ?", userID, since.UTC()).
		Count(&orderCount).Error; err != nil {
		return 0, 0, err
	}
	var receiptCount int64
	if err := r.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Joins("JOIN orders AS o ON o.id = h.order_id").
		Where("o.user_id = ? AND o.service_mode = 'code' AND o.created_at >= ?", userID, since.UTC()).
		Count(&receiptCount).Error; err != nil {
		return 0, 0, err
	}
	return int(orderCount), int(receiptCount), nil
}

func (r *ViewRepo) RangeAvgReceiptSeconds(ctx context.Context, userID uint, from, to time.Time) (int, error) {
	var avg *float64
	if err := r.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Joins("JOIN orders AS o ON o.id = h.order_id").
		Select("GREATEST(ROUND(AVG(TIMESTAMPDIFF(SECOND, o.receive_started_at, h.message_received_at))),0)").
		Where("o.user_id = ? AND o.service_mode = 'code' AND o.created_at >= ? AND o.created_at <= ?", userID, from.UTC(), to.UTC()).
		Scan(&avg).Error; err != nil {
		return 0, err
	}
	if avg == nil {
		return 0, nil
	}
	return int(*avg), nil
}

// Leaderboard ranks users by successfully fulfilled orders: delivered code
// orders plus purchase orders with an activation timestamp. since=nil is all-time.
// Redis serves the periodic snapshot; cache misses fall back to the same live
// query so startup and Redis failures do not blank the dashboard.
func (r *ViewRepo) Leaderboard(ctx context.Context, since *time.Time, limit int) ([]dashboardapp.LeaderRow, error) {
	if limit <= 0 {
		limit = 10
	}
	if rows, ok := r.cachedLeaderboard(ctx, since); ok {
		if len(rows) > limit {
			rows = rows[:limit]
		}
		return rows, nil
	}
	return r.queryLeaderboard(ctx, since, limit)
}

func (r *ViewRepo) queryLeaderboard(ctx context.Context, since *time.Time, limit int) ([]dashboardapp.LeaderRow, error) {
	query := r.db.WithContext(ctx).
		Table("orders AS o").
		Joins("LEFT JOIN mailmatch_order_delivery_heads AS h ON h.order_id = o.id").
		Joins("JOIN users AS u ON u.id = o.user_id").
		Select("o.user_id AS user_id, COALESCE(u.nickname, '') AS nickname, COALESCE(u.email, '') AS email, COUNT(*) AS count").
		Where(successfulOrderPredicate).
		Group("o.user_id, u.nickname, u.email").
		Order("count DESC, o.user_id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if since != nil {
		query = query.Where("o.created_at >= ?", since.UTC())
	}
	var rows []struct {
		UserID   uint   `gorm:"column:user_id"`
		Nickname string `gorm:"column:nickname"`
		Email    string `gorm:"column:email"`
		Count    int    `gorm:"column:count"`
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]dashboardapp.LeaderRow, len(rows))
	for i := range rows {
		out[i] = dashboardapp.LeaderRow(rows[i])
	}
	return out, nil
}

// UserStanding returns the caller's own successful-order count and their ordinal
// position on the same leaderboard the panel renders (ordered by count DESC,
// then user_id ASC), so the "me" row shown when the caller is outside the top
// ranks agrees with the visible list on ties. It also carries the caller's
// identity for that row. since=nil is all-time.
func (r *ViewRepo) UserStanding(ctx context.Context, userID uint, since *time.Time) (dashboardapp.Standing, error) {
	if rows, ok := r.cachedLeaderboard(ctx, since); ok {
		for i, row := range rows {
			if row.UserID == userID {
				return dashboardapp.Standing{Count: row.Count, Rank: i + 1, Nickname: row.Nickname, Email: row.Email}, nil
			}
		}
		identity, err := r.userIdentity(ctx, userID)
		if err != nil {
			return dashboardapp.Standing{}, err
		}
		return dashboardapp.Standing{Rank: len(rows) + 1, Nickname: identity.Nickname, Email: identity.Email}, nil
	}

	countQuery := r.db.WithContext(ctx).
		Table("orders AS o").
		Joins("LEFT JOIN mailmatch_order_delivery_heads AS h ON h.order_id = o.id").
		Where("o.user_id = ? AND "+successfulOrderPredicate, userID)
	if since != nil {
		countQuery = countQuery.Where("o.created_at >= ?", since.UTC())
	}
	var count int64
	if err := countQuery.Count(&count).Error; err != nil {
		return dashboardapp.Standing{}, err
	}

	identity, err := r.userIdentity(ctx, userID)
	if err != nil {
		return dashboardapp.Standing{}, err
	}

	// Users that sort strictly before the caller on the leaderboard order
	// (count DESC, user_id ASC): higher count, or equal count and a smaller id.
	// The caller's own row is excluded (user_id < userID is strict).
	ahead := r.db.WithContext(ctx).
		Table("orders AS o").
		Joins("LEFT JOIN mailmatch_order_delivery_heads AS h ON h.order_id = o.id").
		Select("o.user_id").
		Where(successfulOrderPredicate).
		Group("o.user_id").
		Having("COUNT(*) > ? OR (COUNT(*) = ? AND o.user_id < ?)", count, count, userID)
	if since != nil {
		ahead = ahead.Where("o.created_at >= ?", since.UTC())
	}
	var aheadCount int64
	if err := r.db.WithContext(ctx).Table("(?) AS ranked", ahead).Count(&aheadCount).Error; err != nil {
		return dashboardapp.Standing{}, err
	}

	return dashboardapp.Standing{
		Count:    int(count),
		Rank:     int(aheadCount) + 1,
		Nickname: identity.Nickname,
		Email:    identity.Email,
	}, nil
}

func (r *ViewRepo) RefreshLeaderboardCache(ctx context.Context, todayStart time.Time) error {
	if r.redis == nil {
		return fmt.Errorf("leaderboard cache is unavailable")
	}
	today, err := r.queryLeaderboard(ctx, &todayStart, 0)
	if err != nil {
		return err
	}
	historical, err := r.queryLeaderboard(ctx, nil, 0)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(leaderboardSnapshot{TodayStart: todayStart, Today: today, Historical: historical})
	if err != nil {
		return err
	}
	return r.redis.Set(ctx, leaderboardCacheKey, payload, leaderboardCacheTTL).Err()
}

func (r *ViewRepo) cachedLeaderboard(ctx context.Context, since *time.Time) ([]dashboardapp.LeaderRow, bool) {
	if r.redis == nil {
		return nil, false
	}
	payload, err := r.redis.Get(ctx, leaderboardCacheKey).Bytes()
	if err != nil {
		return nil, false
	}
	var snapshot leaderboardSnapshot
	if json.Unmarshal(payload, &snapshot) != nil {
		return nil, false
	}
	if since == nil {
		return snapshot.Historical, true
	}
	if since.Equal(snapshot.TodayStart) {
		return snapshot.Today, true
	}
	return nil, false
}

func (r *ViewRepo) userIdentity(ctx context.Context, userID uint) (struct {
	Nickname string `gorm:"column:nickname"`
	Email    string `gorm:"column:email"`
}, error) {
	var identity struct {
		Nickname string `gorm:"column:nickname"`
		Email    string `gorm:"column:email"`
	}
	err := r.db.WithContext(ctx).
		Table("users").
		Select("COALESCE(nickname, '') AS nickname, COALESCE(email, '') AS email").
		Where("id = ?", userID).
		Scan(&identity).Error
	return identity, err
}

func parseMoney(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
