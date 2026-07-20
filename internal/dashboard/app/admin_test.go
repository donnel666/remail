package app

import (
	"context"
	"testing"
	"time"
)

type fakeAdminView struct {
	orders     []CountBucket
	codeOrder  []TypeCountBucket
	receipts   []TypeReceiptBucket
	newUsers   []CountBucket
	active     []CountBucket
	totalUsers int
	snapshot   InventorySnapshot
	ranking    []ProjectCountRow
}

func (f *fakeAdminView) OrderTrend(context.Context, string, time.Time, time.Time) ([]CountBucket, error) {
	return f.orders, nil
}
func (f *fakeAdminView) CodeOrderTrend(context.Context, string, time.Time, time.Time) ([]TypeCountBucket, error) {
	return f.codeOrder, nil
}
func (f *fakeAdminView) CodeReceiptTrend(context.Context, string, time.Time, time.Time) ([]TypeReceiptBucket, error) {
	return f.receipts, nil
}
func (f *fakeAdminView) NewUserTrend(context.Context, string, time.Time, time.Time) ([]CountBucket, error) {
	return f.newUsers, nil
}
func (f *fakeAdminView) ActiveUserTrend(context.Context, string, time.Time, time.Time) ([]CountBucket, error) {
	return f.active, nil
}
func (f *fakeAdminView) TotalUsers(context.Context) (int, error) {
	return f.totalUsers, nil
}
func (f *fakeAdminView) InventorySnapshot(context.Context) (InventorySnapshot, error) {
	return f.snapshot, nil
}
func (f *fakeAdminView) ProjectCodeRanking(context.Context, time.Time, time.Time, int) ([]ProjectCountRow, error) {
	return f.ranking, nil
}

type fakeFinance struct{ fin AdminFinance }

func (f fakeFinance) FinanceSummary(context.Context, *time.Time, *time.Time) (AdminFinance, error) {
	return f.fin, nil
}

type fakeInventory struct{ items []AdminInventoryItem }

func (f fakeInventory) ProjectInventoryRanking(context.Context, int) ([]AdminInventoryItem, error) {
	return f.items, nil
}

func TestAdminDashboardAssembly(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	gran := granularity(from, to)
	layout := bucketLayout(gran)
	sameYear := true
	var keys, labels []string
	for bt := bucketStart(from, gran); !bt.After(bucketStart(to, gran)); bt = nextBucket(bt, gran) {
		keys = append(keys, bt.Format(layout))
		labels = append(labels, trendLabel(bt, gran, sameYear))
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(keys))
	}

	view := &fakeAdminView{
		orders: []CountBucket{{Bucket: keys[0], Count: 10}, {Bucket: keys[2], Count: 4}},
		codeOrder: []TypeCountBucket{
			{Bucket: keys[0], ProductType: "microsoft", Count: 6},
			{Bucket: keys[0], ProductType: "domain", Count: 2},
		},
		receipts: []TypeReceiptBucket{
			{Bucket: keys[0], ProductType: "microsoft", Received: 5, AvgSeconds: 20},
			{Bucket: keys[0], ProductType: "domain", Received: 2, AvgSeconds: 40},
		},
		newUsers:   []CountBucket{{Bucket: keys[0], Count: 3}, {Bucket: keys[1], Count: 2}},
		active:     []CountBucket{{Bucket: keys[0], Count: 7}, {Bucket: keys[2], Count: 5}},
		totalUsers: 105,
		snapshot:   InventorySnapshot{MicrosoftTotal: 500, MicrosoftAvailable: 300, DomainTotal: 200, DomainAvailable: 120},
		ranking:    []ProjectCountRow{{ProjectID: 1, Name: "Microsoft", Count: 5}, {ProjectID: 2, Name: "", Count: 2}},
	}
	finance := fakeFinance{fin: AdminFinance{
		RechargeAmount: 1000, SpendAmount: 800, RefundAmount: 20, WithdrawAmount: 60, PlatformRevenue: 120,
		Trend: []AdminFinanceBucket{
			{Label: labels[0], Recharge: 500, Spend: 400, Refund: 10, Withdraw: 30, PlatformRevenue: 60},
			{Label: labels[2], Recharge: 500, Spend: 400, Refund: 10, Withdraw: 30, PlatformRevenue: 60},
		},
	}}
	inventory := fakeInventory{items: []AdminInventoryItem{{Name: "LowStock", Available: 3}, {Name: "Mid", Available: 40}}}

	svc := NewAdminQueryService(view, finance, inventory)
	got, err := svc.AdminDashboard(context.Background(), &from, &to)
	if err != nil {
		t.Fatalf("AdminDashboard: %v", err)
	}

	if len(got.Trend) != 3 {
		t.Fatalf("trend length = %d, want 3", len(got.Trend))
	}
	// finance merged by label; bucket 1 has no finance -> zero.
	if got.Trend[0].RechargeAmount != 500 || got.Trend[1].RechargeAmount != 0 {
		t.Errorf("finance merge wrong: %+v / %+v", got.Trend[0], got.Trend[1])
	}
	// inventory snapshot flat-lined across every bucket.
	for i, p := range got.Trend {
		if p.MicrosoftTotalEmails != 500 || p.DomainAvailableMailboxes != 120 {
			t.Errorf("bucket %d inventory not flat-lined: %+v", i, p)
		}
	}
	// Total users is a current snapshot; only new/active users follow the range.
	if got.Trend[0].TotalUsers != 105 || got.Trend[2].TotalUsers != 105 {
		t.Errorf("total users snapshot wrong: %d / %d", got.Trend[0].TotalUsers, got.Trend[2].TotalUsers)
	}
	if got.Trend[0].ActiveUsers != 7 || got.Trend[0].NewUsers != 3 {
		t.Errorf("bucket0 users: active %d new %d", got.Trend[0].ActiveUsers, got.Trend[0].NewUsers)
	}
	// bucket0 success rates: ms 5/6=83.3, domain 2/2=100.
	if got.Trend[0].MicrosoftCodeSuccessRate != 83.3 || got.Trend[0].DomainCodeSuccessRate != 100 {
		t.Errorf("bucket0 success rates: %v / %v", got.Trend[0].MicrosoftCodeSuccessRate, got.Trend[0].DomainCodeSuccessRate)
	}
	if got.Trend[0].SuccessfulCodeReceipts != 7 {
		t.Errorf("bucket0 successful receipts = %d, want 7", got.Trend[0].SuccessfulCodeReceipts)
	}

	s := got.Stats
	if s.RechargeAmount != 1000 || s.PlatformRevenue != 120 {
		t.Errorf("finance stats: %+v", s)
	}
	if s.TotalOrders != 14 || s.SuccessfulCodeReceipts != 7 {
		t.Errorf("order/receipt totals: %d / %d", s.TotalOrders, s.SuccessfulCodeReceipts)
	}
	if s.TotalUsers != 105 || s.NewUsers != 5 || s.ActiveUsers != 12 {
		t.Errorf("user stats: total %d new %d active %d", s.TotalUsers, s.NewUsers, s.ActiveUsers)
	}
	if s.MicrosoftCodeReceipts != 5 || s.DomainCodeReceipts != 2 {
		t.Errorf("code receipt totals: ms %d domain %d", s.MicrosoftCodeReceipts, s.DomainCodeReceipts)
	}
	if s.MicrosoftAverageCodeReceiptSeconds != 20 || s.DomainAverageCodeReceiptSeconds != 40 {
		t.Errorf("avg seconds: ms %d domain %d", s.MicrosoftAverageCodeReceiptSeconds, s.DomainAverageCodeReceiptSeconds)
	}
	if s.MicrosoftTotalEmails != 500 || s.DomainAvailableMailboxes != 120 {
		t.Errorf("inventory stats wrong: %+v", s)
	}

	if len(got.ProjectCodeRanking) != 2 || got.ProjectCodeRanking[0].Rank != 1 || got.ProjectCodeRanking[1].Name != "#2" {
		t.Errorf("code ranking wrong: %+v", got.ProjectCodeRanking)
	}
	if len(got.ProjectInventoryRanking) != 2 || got.ProjectInventoryRanking[0].Name != "LowStock" || got.ProjectInventoryRanking[0].Rank != 1 {
		t.Errorf("inventory ranking wrong: %+v", got.ProjectInventoryRanking)
	}
}
