package app

import (
	"context"
	"testing"
	"time"
)

// fakeView returns canned aggregate rows so the assembly logic (zero-fill,
// ratios, rank assignment, leaderboard name resolution) can be tested without a
// database.
type fakeView struct {
	orders      []OrderBucketRow
	receipts    []ReceiptBucketRow
	activations []PurchaseActivationBucketRow
	ranking     []ProjectCountRow
	spend       []ProjectSpendRow
	balance     float64
	spent       float64
	avgSecs     int
	leaders     []LeaderRow
	standing    Standing
	userID      uint
	from        time.Time
	to          time.Time
	fullReads   int
}

func (f *fakeView) WalletSummary(_ context.Context, userID uint) (float64, float64, error) {
	f.userID = userID
	return f.balance, f.spent, nil
}
func (f *fakeView) OrderBuckets(_ context.Context, userID uint, _ string, from, to time.Time) ([]OrderBucketRow, error) {
	f.userID, f.from, f.to = userID, from, to
	return f.orders, nil
}
func (f *fakeView) ReceiptBuckets(_ context.Context, userID uint, _ string, _, _ time.Time) ([]ReceiptBucketRow, error) {
	f.userID = userID
	return f.receipts, nil
}
func (f *fakeView) PurchaseActivationBuckets(_ context.Context, userID uint, _ string, _, _ time.Time) ([]PurchaseActivationBucketRow, error) {
	f.userID = userID
	return f.activations, nil
}
func (f *fakeView) ProjectCodeRanking(context.Context, uint, time.Time, time.Time) ([]ProjectCountRow, error) {
	f.fullReads++
	return f.ranking, nil
}
func (f *fakeView) ProjectSpendBuckets(context.Context, uint, string, time.Time, time.Time) ([]ProjectSpendRow, error) {
	f.fullReads++
	return f.spend, nil
}
func (f *fakeView) RangeAvgReceiptSeconds(_ context.Context, userID uint, _, _ time.Time) (int, error) {
	f.userID = userID
	return f.avgSecs, nil
}
func (f *fakeView) Leaderboard(context.Context, *time.Time, int) ([]LeaderRow, error) {
	f.fullReads++
	return f.leaders, nil
}
func (f *fakeView) UserStanding(context.Context, uint, *time.Time) (Standing, error) {
	f.fullReads++
	return f.standing, nil
}

func TestConsoleDashboardAssembly(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	// Compute the exact bucket keys the service will iterate, using the same
	// helpers, so the fake's rows line up regardless of the host time zone.
	gran := granularity(from, to)
	layout := bucketLayout(gran)
	var keys []string
	for bt := bucketStart(from, gran); !bt.After(bucketStart(to, gran)); bt = nextBucket(bt, gran) {
		keys = append(keys, bt.Format(layout))
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 daily buckets, got %d", len(keys))
	}

	ranking := make([]ProjectCountRow, 8)
	for i := range ranking {
		ranking[i] = ProjectCountRow{ProjectID: uint(i + 1), Name: "P" + string(rune('A'+i)), Count: 20 - i}
	}
	spend := make([]ProjectSpendRow, 8)
	for i := range spend {
		spend[i] = ProjectSpendRow{
			ProjectID: uint(100 + i),
			Name:      "Spend " + string(rune('A'+i)),
			Bucket:    keys[0],
			Spend:     float64(10 - i),
		}
	}

	view := &fakeView{
		// orders in bucket 0 and 2, none in bucket 1 (must zero-fill).
		orders: []OrderBucketRow{
			{Bucket: keys[0], Orders: 10, CodeOrders: 6, PurchaseOrders: 4, Spend: 12.005},
			{Bucket: keys[2], Orders: 4, CodeOrders: 3, PurchaseOrders: 1, Spend: 5.5},
		},
		receipts: []ReceiptBucketRow{
			{Bucket: keys[0], Received: 4, AvgSeconds: 30},
			{Bucket: keys[2], Received: 1, AvgSeconds: 50},
		},
		activations: []PurchaseActivationBucketRow{
			{Bucket: keys[0], Activated: 2, AvgSeconds: 20, TotalSeconds: 40, Timed: 2},
			{Bucket: keys[2], Activated: 1, AvgSeconds: 40, TotalSeconds: 40, Timed: 1},
		},
		ranking: ranking,
		spend:   spend,
		balance: 640.123,
		spent:   1200.5,
		avgSecs: 42,
		leaders: []LeaderRow{
			{UserID: 7, Nickname: "", Email: "alice@example.com", Count: 20},
			{UserID: 42, Nickname: "Me", Email: "me@example.com", Count: 9},
		},
		standing: Standing{Count: 9, Rank: 2, Nickname: "Me", Email: "me@example.com"},
	}

	svc := NewQueryService(view)
	got, err := svc.ConsoleDashboard(context.Background(), 42, &from, &to)
	if err != nil {
		t.Fatalf("ConsoleDashboard: %v", err)
	}

	if len(got.Trend) != 3 {
		t.Fatalf("trend length = %d, want 3", len(got.Trend))
	}
	if got.Trend[1].Orders != 0 || got.Trend[1].ReceivedCodes != 0 || got.Trend[1].ActivatedPurchases != 0 {
		t.Errorf("bucket 1 = %+v, want zero-filled fulfillment metrics", got.Trend[1])
	}
	if got.Trend[0].Spend != 12.01 { // 12.005 rounds to 12.01
		t.Errorf("bucket 0 spend = %v, want 12.01", got.Trend[0].Spend)
	}
	// codeSuccessRate = receipts(5) / codeOrders(9) * 100 = 55.6 (1dp).
	if got.Stats.CodeSuccessRate != 55.6 {
		t.Errorf("codeSuccessRate = %v, want 55.6", got.Stats.CodeSuccessRate)
	}
	if got.Stats.PurchaseActivationSuccessRate != 60 || got.Stats.AveragePurchaseActivationSeconds != 27 {
		t.Errorf("purchase fulfillment = %v%% / %ds, want 60%% / 27s", got.Stats.PurchaseActivationSuccessRate, got.Stats.AveragePurchaseActivationSeconds)
	}
	// codeRatio = codeOrders(9) / orders(14) * 100 = round(64.28) = 64.
	if got.CodeRatio != 64 || got.PurchaseRatio != 36 {
		t.Errorf("ratios = code %v / purchase %v, want 64/36", got.CodeRatio, got.PurchaseRatio)
	}
	if got.Stats.WalletBalance != 640.12 || got.Stats.HistoricalSpend != 1200.5 {
		t.Errorf("wallet = %v / %v", got.Stats.WalletBalance, got.Stats.HistoricalSpend)
	}
	if len(got.ProjectCodeRanking) != 8 || got.ProjectCodeRanking[0].Rank != 1 || got.ProjectCodeRanking[7].Rank != 8 {
		t.Errorf("ranking ranks not assigned 1..8: %+v", got.ProjectCodeRanking)
	}
	if len(got.ProjectSeries) != projectSeriesLimit {
		t.Fatalf("project series = %d, want %d", len(got.ProjectSeries), projectSeriesLimit)
	}
	for _, s := range got.ProjectSeries {
		if len(s.Spend) != 3 {
			t.Errorf("series %q spend length = %d, want 3", s.Name, len(s.Spend))
		}
	}
	if got.ProjectSeries[0].Name != "Spend A" || got.ProjectSeries[0].Spend[0] != 10 {
		t.Errorf("series[0] = %+v, want independent top-spend project", got.ProjectSeries[0])
	}
	// leaderboard: alice has no nickname -> email local part; row 2 is me.
	if got.TodayCodeRanking[0].Name != "alice" || got.TodayCodeRanking[0].IsCurrentUser {
		t.Errorf("leader 0 = %+v, want alice not current user", got.TodayCodeRanking[0])
	}
	if !got.TodayCodeRanking[1].IsCurrentUser {
		t.Errorf("leader 1 should be current user: %+v", got.TodayCodeRanking[1])
	}
	if got.TodayCurrentUserRank.Rank != 2 || !got.TodayCurrentUserRank.IsCurrentUser || got.TodayCurrentUserRank.Name != "Me" {
		t.Errorf("current user rank = %+v, want rank 2 Me", got.TodayCurrentUserRank)
	}
}

func TestConsoleStatsUsesSelectedUserAndSkipsFullDashboardReads(t *testing.T) {
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	view := &fakeView{
		orders:      []OrderBucketRow{{Orders: 5, CodeOrders: 3, PurchaseOrders: 2}},
		receipts:    []ReceiptBucketRow{{Received: 2}},
		activations: []PurchaseActivationBucketRow{{Activated: 1, TotalSeconds: 27, Timed: 1}},
		balance:     2.48,
		spent:       0.02,
		avgSecs:     9,
	}

	stats, err := NewQueryService(view).ConsoleStats(context.Background(), 42, &from, &to)
	if err != nil {
		t.Fatalf("ConsoleStats: %v", err)
	}
	if view.userID != 42 || !view.from.Equal(from) || !view.to.Equal(to) {
		t.Fatalf("query scope = user %d [%s,%s], want user 42 [%s,%s]", view.userID, view.from, view.to, from, to)
	}
	if view.fullReads != 0 {
		t.Fatalf("full dashboard reads = %d, want 0", view.fullReads)
	}
	if stats.WalletBalance != 2.48 || stats.HistoricalSpend != 0.02 || stats.CodeSuccessRate != 66.7 || stats.AverageCodeReceiptSeconds != 9 || stats.PurchaseActivationSuccessRate != 50 || stats.AveragePurchaseActivationSeconds != 27 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		nickname, email string
		id              uint
		want            string
	}{
		{"Alice", "alice@example.com", 1, "Alice"},
		{"  ", "bob@example.com", 2, "bob"},
		{"", "carol@sub.example.com", 3, "carol"},
		{"", "", 5, "#5"},
		{"", "weird", 6, "weird"},
	}
	for _, c := range cases {
		if got := displayName(c.nickname, c.email, c.id); got != c.want {
			t.Errorf("displayName(%q,%q,%d) = %q, want %q", c.nickname, c.email, c.id, got, c.want)
		}
	}
}

func TestTodayStartUsesShanghaiDay(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 42, 0, 0, time.UTC)
	want := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	if got := TodayStart(now); !got.Equal(want) {
		t.Fatalf("TodayStart(%s) = %s, want %s", now, got, want)
	}
}

func TestDashboardBucketsUseShanghaiDay(t *testing.T) {
	now := time.Date(2026, 7, 21, 15, 42, 0, 0, time.UTC)
	want := time.Date(2026, 7, 21, 0, 0, 0, 0, dashboardLocation)
	if got := bucketStart(now, "day"); !got.Equal(want) {
		t.Fatalf("bucketStart(%s) = %s, want %s", now, got, want)
	}
	if got := granularity(now, now); got != "hour" {
		t.Fatalf("granularity(%s,%s) = %q, want hour", now, now, got)
	}
}
