// Package app assembles the read-only console data dashboard from raw
// aggregate rows produced by the infra ViewRepo. All business logic that the
// mock previously did on the client (time bucketing, zero-fill, labels,
// ratios, rank assignment and leaderboard name resolution) lives here so the
// frontend presentation stays untouched.
package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// bucketing constants mirror internal/billing/app/finance.go so the dashboard
// trend labels and hourly/daily granularity match the finance summary exactly.
const (
	maxTrendBuckets    = 2000
	defaultSummaryDays = 1
	// projectSeriesLimit caps how many top projects get a spend series, matching
	// the mock's featured = ranks.slice(0, 6).
	projectSeriesLimit = 6
	// leaderboardLimit is the number of ranked users the panel renders (two
	// columns of five).
	leaderboardLimit = 10
)

// ---- raw aggregate rows returned by the ViewRepo -------------------------

type OrderBucketRow struct {
	Bucket     string
	Orders     int
	CodeOrders int
	Spend      float64
}

type ReceiptBucketRow struct {
	Bucket     string
	Received   int
	AvgSeconds int
}

type ProjectCountRow struct {
	ProjectID uint
	Name      string
	Count     int
}

type ProjectSpendRow struct {
	ProjectID uint
	Bucket    string
	Spend     float64
}

type LeaderRow struct {
	UserID   uint
	Nickname string
	Email    string
	Count    int
}

type Standing struct {
	Count    int
	Rank     int
	Nickname string
	Email    string
}

// ConsoleView is the read port implemented by infra.ViewRepo. Every method is a
// single read-only aggregate; the SQL format string is a fixed internal
// constant ("%Y-%m-%d %H:00:00" or "%Y-%m-%d"), never user input.
type ConsoleView interface {
	WalletSummary(ctx context.Context, userID uint) (balance, totalSpend float64, err error)
	OrderBuckets(ctx context.Context, userID uint, sqlFormat string, from, to time.Time) ([]OrderBucketRow, error)
	ReceiptBuckets(ctx context.Context, userID uint, sqlFormat string, from, to time.Time) ([]ReceiptBucketRow, error)
	ProjectCodeRanking(ctx context.Context, userID uint, from, to time.Time) ([]ProjectCountRow, error)
	ProjectSpendBuckets(ctx context.Context, userID uint, projectIDs []uint, sqlFormat string, from, to time.Time) ([]ProjectSpendRow, error)
	TodayCounts(ctx context.Context, userID uint, since time.Time) (orders, receipts int, err error)
	RangeAvgReceiptSeconds(ctx context.Context, userID uint, from, to time.Time) (int, error)
	Leaderboard(ctx context.Context, since *time.Time, limit int) ([]LeaderRow, error)
	UserStanding(ctx context.Context, userID uint, since *time.Time) (Standing, error)
}

// ---- assembled read model (mapped to the API DTO 1:1) --------------------

type Stats struct {
	WalletBalance             float64
	HistoricalSpend           float64
	TodayOrders               int
	TotalOrders               int
	TodayCodeReceipts         int
	TotalCodeReceipts         int
	CodeSuccessRate           float64
	AverageCodeReceiptSeconds int
}

type TrendPoint struct {
	Label                     string
	Orders                    int
	CodeOrders                int
	ReceivedCodes             int
	AverageCodeReceiptSeconds int
	Spend                     float64
}

type ProjectSeries struct {
	Name  string
	Spend []float64
}

type RankItem struct {
	Name          string
	Count         int
	Rank          int
	IsCurrentUser bool
}

type ConsoleDashboard struct {
	Stats                     Stats
	Trend                     []TrendPoint
	ProjectSeries             []ProjectSeries
	ProjectCodeRanking        []RankItem
	CodeRatio                 float64
	PurchaseRatio             float64
	TodayCodeRanking          []RankItem
	HistoricalCodeRanking     []RankItem
	TodayCurrentUserRank      RankItem
	HistoricalCurrentUserRank RankItem
}

// QueryService builds the console dashboard for one user.
type QueryService struct {
	view ConsoleView
	now  func() time.Time
}

func NewQueryService(view ConsoleView) *QueryService {
	return &QueryService{view: view, now: time.Now}
}

// ConsoleDashboard aggregates the signed-in user's overview over [from, to].
// "today" metrics and the today leaderboard are always relative to the real
// current day, independent of the selected range, matching the mock.
func (s *QueryService) ConsoleDashboard(ctx context.Context, userID uint, from, to *time.Time) (*ConsoleDashboard, error) {
	now := s.now()
	fromT, toT := resolveRange(from, to, now)
	gran := granularity(fromT, toT)
	sqlFmt := sqlFormat(gran)
	layout := bucketLayout(gran)
	sameYear := fromT.In(time.Local).Year() == toT.In(time.Local).Year()
	today := startOfToday(now)

	orderRows, err := s.view.OrderBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	receiptRows, err := s.view.ReceiptBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	ranking, err := s.view.ProjectCodeRanking(ctx, userID, fromT, toT)
	if err != nil {
		return nil, err
	}
	featured := ranking
	if len(featured) > projectSeriesLimit {
		featured = featured[:projectSeriesLimit]
	}
	featuredIDs := make([]uint, len(featured))
	for i, p := range featured {
		featuredIDs[i] = p.ProjectID
	}
	spendRows, err := s.view.ProjectSpendBuckets(ctx, userID, featuredIDs, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	todayOrders, todayReceipts, err := s.view.TodayCounts(ctx, userID, today)
	if err != nil {
		return nil, err
	}
	avgSeconds, err := s.view.RangeAvgReceiptSeconds(ctx, userID, fromT, toT)
	if err != nil {
		return nil, err
	}
	balance, historicalSpend, err := s.view.WalletSummary(ctx, userID)
	if err != nil {
		return nil, err
	}
	todayLeaders, err := s.view.Leaderboard(ctx, &today, leaderboardLimit)
	if err != nil {
		return nil, err
	}
	historicalLeaders, err := s.view.Leaderboard(ctx, nil, leaderboardLimit)
	if err != nil {
		return nil, err
	}
	todayStanding, err := s.view.UserStanding(ctx, userID, &today)
	if err != nil {
		return nil, err
	}
	historicalStanding, err := s.view.UserStanding(ctx, userID, nil)
	if err != nil {
		return nil, err
	}

	orderByKey := make(map[string]OrderBucketRow, len(orderRows))
	for _, r := range orderRows {
		orderByKey[r.Bucket] = r
	}
	receiptByKey := make(map[string]ReceiptBucketRow, len(receiptRows))
	for _, r := range receiptRows {
		receiptByKey[r.Bucket] = r
	}
	spendByProject := make(map[uint]map[string]float64, len(featuredIDs))
	for _, r := range spendRows {
		if spendByProject[r.ProjectID] == nil {
			spendByProject[r.ProjectID] = make(map[string]float64)
		}
		spendByProject[r.ProjectID][r.Bucket] = r.Spend
	}

	trend := make([]TrendPoint, 0, len(orderRows)+len(receiptRows))
	seriesSpend := make(map[uint][]float64, len(featuredIDs))
	var totalOrders, totalCodeOrders, totalReceipts int
	for t := bucketStart(fromT, gran); !t.After(bucketStart(toT, gran)) && len(trend) < maxTrendBuckets; t = nextBucket(t, gran) {
		key := t.Format(layout)
		o := orderByKey[key]
		r := receiptByKey[key]
		trend = append(trend, TrendPoint{
			Label:                     trendLabel(t, gran, sameYear),
			Orders:                    o.Orders,
			CodeOrders:                o.CodeOrders,
			ReceivedCodes:             r.Received,
			AverageCodeReceiptSeconds: r.AvgSeconds,
			Spend:                     roundMoney(o.Spend),
		})
		totalOrders += o.Orders
		totalCodeOrders += o.CodeOrders
		totalReceipts += r.Received
		for _, id := range featuredIDs {
			seriesSpend[id] = append(seriesSpend[id], roundMoney(spendByProject[id][key]))
		}
	}
	if trend == nil {
		trend = []TrendPoint{}
	}

	projectSeries := make([]ProjectSeries, len(featured))
	for i, p := range featured {
		projectSeries[i] = ProjectSeries{Name: projectLabel(p), Spend: seriesSpend[p.ProjectID]}
	}

	projectCodeRanking := make([]RankItem, len(ranking))
	for i, p := range ranking {
		projectCodeRanking[i] = RankItem{Name: projectLabel(p), Count: p.Count, Rank: i + 1}
	}

	codeRatio := roundInt(pct(totalCodeOrders, totalOrders))
	purchaseRatio := 0.0
	if totalOrders > 0 {
		purchaseRatio = 100 - codeRatio
	}

	return &ConsoleDashboard{
		Stats: Stats{
			WalletBalance:     roundMoney(balance),
			HistoricalSpend:   roundMoney(historicalSpend),
			TodayOrders:       todayOrders,
			TotalOrders:       totalOrders,
			TodayCodeReceipts: todayReceipts,
			TotalCodeReceipts: totalReceipts,
			// Receipts are keyed by receipt time and code orders by creation time,
			// so a code received in-range for an order created just before it can
			// nudge the ratio slightly over 100; cap for a sane percentage.
			CodeSuccessRate:           round1(math.Min(100, pct(totalReceipts, totalCodeOrders))),
			AverageCodeReceiptSeconds: avgSeconds,
		},
		Trend:                     trend,
		ProjectSeries:             projectSeries,
		ProjectCodeRanking:        projectCodeRanking,
		CodeRatio:                 codeRatio,
		PurchaseRatio:             purchaseRatio,
		TodayCodeRanking:          rankLeaders(todayLeaders, userID),
		HistoricalCodeRanking:     rankLeaders(historicalLeaders, userID),
		TodayCurrentUserRank:      standingItem(todayStanding, userID),
		HistoricalCurrentUserRank: standingItem(historicalStanding, userID),
	}, nil
}

func rankLeaders(rows []LeaderRow, currentUserID uint) []RankItem {
	items := make([]RankItem, len(rows))
	for i, r := range rows {
		items[i] = RankItem{
			Name:          displayName(r.Nickname, r.Email, r.UserID),
			Count:         r.Count,
			Rank:          i + 1,
			IsCurrentUser: r.UserID == currentUserID,
		}
	}
	return items
}

func standingItem(s Standing, currentUserID uint) RankItem {
	return RankItem{
		Name:          displayName(s.Nickname, s.Email, currentUserID),
		Count:         s.Count,
		Rank:          s.Rank,
		IsCurrentUser: true,
	}
}

func projectLabel(p ProjectCountRow) string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return fmt.Sprintf("#%d", p.ProjectID)
}

// displayName resolves the leaderboard label: the trimmed nickname when set,
// otherwise the email local-part (before "@"), otherwise a user tag. The email
// domain suffix is never exposed.
func displayName(nickname, email string, userID uint) string {
	if n := strings.TrimSpace(nickname); n != "" {
		return n
	}
	if local, _, ok := strings.Cut(strings.TrimSpace(email), "@"); ok && local != "" {
		return local
	}
	if e := strings.TrimSpace(email); e != "" {
		return e
	}
	return fmt.Sprintf("#%d", userID)
}

// ---- bucketing helpers (mirrors internal/billing/app/finance.go) ---------

func resolveRange(from, to *time.Time, now time.Time) (time.Time, time.Time) {
	toT := now.UTC()
	if to != nil {
		toT = to.UTC()
	}
	fromT := toT.AddDate(0, 0, -defaultSummaryDays)
	if from != nil {
		fromT = from.UTC()
	}
	if fromT.After(toT) {
		fromT = toT
	}
	// Clamp so daily buckets never exceed maxTrendBuckets; the SQL still
	// aggregates the whole span, so an unclamped range would under-report totals
	// once the trend loop caps. A span of N days spans N+1 inclusive daily
	// buckets, so cap the span at maxTrendBuckets-1 days.
	if maxSpan := time.Duration(maxTrendBuckets-1) * 24 * time.Hour; toT.Sub(fromT) > maxSpan {
		fromT = toT.Add(-maxSpan)
	}
	return fromT, toT
}

func granularity(from, to time.Time) string {
	fl, tl := from.In(time.Local), to.In(time.Local)
	if fl.Year() == tl.Year() && fl.YearDay() == tl.YearDay() {
		return "hour"
	}
	return "day"
}

func sqlFormat(gran string) string {
	if gran == "hour" {
		return "%Y-%m-%d %H:00:00"
	}
	return "%Y-%m-%d"
}

func bucketLayout(gran string) string {
	if gran == "hour" {
		return "2006-01-02 15:00:00"
	}
	return "2006-01-02"
}

func bucketStart(t time.Time, gran string) time.Time {
	t = t.In(time.Local)
	if gran == "hour" {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.Local)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func nextBucket(t time.Time, gran string) time.Time {
	if gran == "hour" {
		return t.Add(time.Hour)
	}
	return t.AddDate(0, 0, 1)
}

func trendLabel(t time.Time, gran string, sameYear bool) string {
	if gran == "hour" {
		return fmt.Sprintf("%02d:00", t.Hour())
	}
	if sameYear {
		return fmt.Sprintf("%d/%d", int(t.Month()), t.Day())
	}
	return fmt.Sprintf("%d/%d/%d", t.Year(), int(t.Month()), t.Day())
}

func startOfToday(now time.Time) time.Time {
	l := now.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.Local)
}

// ---- numeric helpers -----------------------------------------------------

func pct(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

func roundMoney(v float64) float64 {
	if v < 0 {
		v = 0
	}
	return math.Round(v*100) / 100
}

func roundInt(v float64) float64 { return math.Round(v) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
