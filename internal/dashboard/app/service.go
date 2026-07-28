// Package app assembles the read-only console data dashboard from raw
// aggregate rows produced by the infra ViewRepo. All business logic that the
// mock previously did on the client (time bucketing, zero-fill, labels,
// ratios, rank assignment and leaderboard name resolution) lives here so the
// frontend remains a thin presentation layer.
package app

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/businessday"
)

// bucketing constants mirror internal/billing/app/finance.go so the dashboard
// trend labels and hourly/daily granularity match the finance summary exactly.
const (
	maxTrendBuckets    = 2000
	defaultSummaryDays = 1
	// projectSeriesLimit caps how many top net-spend projects get a series.
	projectSeriesLimit = 6
	// leaderboardLimit is the number of ranked users the panel renders (two
	// columns of five).
	leaderboardLimit = 10
)

var dashboardLocation = businessday.Shanghai

// ---- raw aggregate rows returned by the ViewRepo -------------------------

type OrderBucketRow struct {
	Bucket         string
	Orders         int
	CodeOrders     int
	PurchaseOrders int
	Spend          float64
}

type ReceiptBucketRow struct {
	Bucket     string
	Received   int
	AvgSeconds int
}

type PurchaseActivationBucketRow struct {
	Bucket       string
	Activated    int
	AvgSeconds   int
	TotalSeconds int64
	Timed        int
}

type ProjectCountRow struct {
	ProjectID uint
	Name      string
	Count     int
}

type ProjectSpendRow struct {
	ProjectID uint
	Name      string
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
	PurchaseActivationBuckets(ctx context.Context, userID uint, sqlFormat string, from, to time.Time) ([]PurchaseActivationBucketRow, error)
	ProjectCodeRanking(ctx context.Context, userID uint, from, to time.Time) ([]ProjectCountRow, error)
	ProjectSpendBuckets(ctx context.Context, userID uint, sqlFormat string, from, to time.Time) ([]ProjectSpendRow, error)
	RangeAvgReceiptSeconds(ctx context.Context, userID uint, from, to time.Time) (int, error)
	Leaderboard(ctx context.Context, since *time.Time, limit int) ([]LeaderRow, error)
	UserStanding(ctx context.Context, userID uint, since *time.Time) (Standing, error)
}

// ---- assembled read model (mapped to the API DTO 1:1) --------------------

type Stats struct {
	WalletBalance                    float64
	HistoricalSpend                  float64
	CodeSuccessRate                  float64
	AverageCodeReceiptSeconds        int
	PurchaseActivationSuccessRate    float64
	AveragePurchaseActivationSeconds int
}

type fulfillmentTotals struct {
	orders, codeOrders, purchaseOrders int
	receipts, activations              int
	activationSeconds                  int64
	timedActivations                   int
}

func totalsFromRows(orderRows []OrderBucketRow, receiptRows []ReceiptBucketRow, activationRows []PurchaseActivationBucketRow) fulfillmentTotals {
	var totals fulfillmentTotals
	for _, row := range orderRows {
		totals.orders += row.Orders
		totals.codeOrders += row.CodeOrders
		totals.purchaseOrders += row.PurchaseOrders
	}
	for _, row := range receiptRows {
		totals.receipts += row.Received
	}
	for _, row := range activationRows {
		totals.activations += row.Activated
		totals.activationSeconds += row.TotalSeconds
		totals.timedActivations += row.Timed
	}
	return totals
}

func (totals fulfillmentTotals) stats(balance, historicalSpend float64, averageCodeReceiptSeconds int) Stats {
	return Stats{
		WalletBalance:                    roundMoney(balance),
		HistoricalSpend:                  roundMoney(historicalSpend),
		CodeSuccessRate:                  round1(math.Min(100, pct(totals.receipts, totals.codeOrders))),
		AverageCodeReceiptSeconds:        averageCodeReceiptSeconds,
		PurchaseActivationSuccessRate:    round1(math.Min(100, pct(totals.activations, totals.purchaseOrders))),
		AveragePurchaseActivationSeconds: averageSeconds(totals.activationSeconds, totals.timedActivations),
	}
}

type TrendPoint struct {
	Label                            string
	Orders                           int
	CodeOrders                       int
	PurchaseOrders                   int
	ReceivedCodes                    int
	ActivatedPurchases               int
	AverageCodeReceiptSeconds        int
	AveragePurchaseActivationSeconds int
	Spend                            float64
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

// ConsoleStats returns only the six account and fulfillment values used by
// compact user summaries, avoiding project and leaderboard work.
func (s *QueryService) ConsoleStats(ctx context.Context, userID uint, from, to *time.Time) (*Stats, error) {
	fromT, toT := resolveRange(from, to, s.now())
	sqlFmt := sqlFormat(granularity(fromT, toT))
	orderRows, err := s.view.OrderBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	receiptRows, err := s.view.ReceiptBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	activationRows, err := s.view.PurchaseActivationBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	averageCodeReceiptSeconds, err := s.view.RangeAvgReceiptSeconds(ctx, userID, fromT, toT)
	if err != nil {
		return nil, err
	}
	balance, historicalSpend, err := s.view.WalletSummary(ctx, userID)
	if err != nil {
		return nil, err
	}
	stats := totalsFromRows(orderRows, receiptRows, activationRows).stats(balance, historicalSpend, averageCodeReceiptSeconds)
	return &stats, nil
}

// ConsoleDashboard aggregates the signed-in user's overview over [from, to].
// The today leaderboard is always relative to the real current day,
// independent of the selected range.
func (s *QueryService) ConsoleDashboard(ctx context.Context, userID uint, from, to *time.Time) (*ConsoleDashboard, error) {
	now := s.now()
	fromT, toT := resolveRange(from, to, now)
	gran := granularity(fromT, toT)
	sqlFmt := sqlFormat(gran)
	layout := bucketLayout(gran)
	sameYear := fromT.In(dashboardLocation).Year() == toT.In(dashboardLocation).Year()
	today := TodayStart(now)

	orderRows, err := s.view.OrderBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	receiptRows, err := s.view.ReceiptBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	activationRows, err := s.view.PurchaseActivationBuckets(ctx, userID, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	totals := totalsFromRows(orderRows, receiptRows, activationRows)
	ranking, err := s.view.ProjectCodeRanking(ctx, userID, fromT, toT)
	if err != nil {
		return nil, err
	}
	spendRows, err := s.view.ProjectSpendBuckets(ctx, userID, sqlFmt, fromT, toT)
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
	activationByKey := make(map[string]PurchaseActivationBucketRow, len(activationRows))
	for _, r := range activationRows {
		activationByKey[r.Bucket] = r
	}
	spendByProject := make(map[uint]map[string]float64)
	spendTotals := make(map[uint]float64)
	spendNames := make(map[uint]string)
	for _, r := range spendRows {
		if spendByProject[r.ProjectID] == nil {
			spendByProject[r.ProjectID] = make(map[string]float64)
		}
		spendByProject[r.ProjectID][r.Bucket] = r.Spend
		spendTotals[r.ProjectID] += r.Spend
		spendNames[r.ProjectID] = r.Name
	}

	// ponytail: sort the current user's grouped project rows in memory; move the
	// top-N ranking into SQL only if per-user project counts become materially large.
	featured := make([]ProjectSpendRow, 0, len(spendTotals))
	for projectID, total := range spendTotals {
		if total > 0 {
			featured = append(featured, ProjectSpendRow{ProjectID: projectID, Name: spendNames[projectID], Spend: total})
		}
	}
	slices.SortFunc(featured, func(left, right ProjectSpendRow) int {
		if order := cmp.Compare(right.Spend, left.Spend); order != 0 {
			return order
		}
		return cmp.Compare(left.ProjectID, right.ProjectID)
	})
	if len(featured) > projectSeriesLimit {
		featured = featured[:projectSeriesLimit]
	}
	featuredIDs := make([]uint, len(featured))
	for i, project := range featured {
		featuredIDs[i] = project.ProjectID
	}

	trend := make([]TrendPoint, 0, len(orderRows)+len(receiptRows))
	seriesSpend := make(map[uint][]float64, len(featuredIDs))
	for t := bucketStart(fromT, gran); !t.After(bucketStart(toT, gran)) && len(trend) < maxTrendBuckets; t = nextBucket(t, gran) {
		key := t.Format(layout)
		o := orderByKey[key]
		r := receiptByKey[key]
		a := activationByKey[key]
		trend = append(trend, TrendPoint{
			Label:                            trendLabel(t, gran, sameYear),
			Orders:                           o.Orders,
			CodeOrders:                       o.CodeOrders,
			PurchaseOrders:                   o.PurchaseOrders,
			ReceivedCodes:                    r.Received,
			ActivatedPurchases:               a.Activated,
			AverageCodeReceiptSeconds:        r.AvgSeconds,
			AveragePurchaseActivationSeconds: a.AvgSeconds,
			Spend:                            roundMoney(o.Spend),
		})
		for _, id := range featuredIDs {
			seriesSpend[id] = append(seriesSpend[id], roundMoney(spendByProject[id][key]))
		}
	}
	if trend == nil {
		trend = []TrendPoint{}
	}

	projectSeries := make([]ProjectSeries, len(featured))
	for i, p := range featured {
		projectSeries[i] = ProjectSeries{Name: projectLabel(p.ProjectID, p.Name), Spend: seriesSpend[p.ProjectID]}
	}

	projectCodeRanking := make([]RankItem, len(ranking))
	for i, p := range ranking {
		projectCodeRanking[i] = RankItem{Name: projectLabel(p.ProjectID, p.Name), Count: p.Count, Rank: i + 1}
	}

	codeRatio := roundInt(pct(totals.codeOrders, totals.orders))
	purchaseRatio := 0.0
	if totals.orders > 0 {
		purchaseRatio = 100 - codeRatio
	}

	return &ConsoleDashboard{
		Stats:                     totals.stats(balance, historicalSpend, avgSeconds),
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

func projectLabel(projectID uint, projectName string) string {
	if name := strings.TrimSpace(projectName); name != "" {
		return name
	}
	return fmt.Sprintf("#%d", projectID)
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
	toT := now.In(dashboardLocation)
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
	fl, tl := from.In(dashboardLocation), to.In(dashboardLocation)
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
	t = t.In(dashboardLocation)
	if gran == "hour" {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, dashboardLocation)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, dashboardLocation)
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

// TodayStart returns midnight in the dashboard's business timezone.
func TodayStart(now time.Time) time.Time {
	_, start, _ := businessday.Bounds(now)
	return start
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

func averageSeconds(total int64, count int) int {
	if count <= 0 {
		return 0
	}
	return max(0, int(math.Round(float64(total)/float64(count))))
}

func roundInt(v float64) float64 { return math.Round(v) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
