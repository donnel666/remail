package app

import (
	"context"
	"time"
)

// Admin platform dashboard. Reuses the bucketing/label helpers in service.go.
// Money comes from billing (AdminFinancePort) and per-project inventory from
// alloc (AdminInventoryPort); everything else is raw global aggregates via
// AdminView. Field names mirror admin-dashboard-mock.ts (consumed fields only).

// ---- ports ---------------------------------------------------------------

// AdminFinancePort adapts billing's finance summary (recharge/spend/refund/
// withdraw/platform revenue) so the money numbers match /admin/finance/summary.
type AdminFinancePort interface {
	FinanceSummary(ctx context.Context, from, to *time.Time) (AdminFinance, error)
}

type AdminFinance struct {
	RechargeAmount  float64
	SpendAmount     float64
	RefundAmount    float64
	WithdrawAmount  float64
	PlatformRevenue float64
	Trend           []AdminFinanceBucket
}

type AdminFinanceBucket struct {
	Label           string
	Recharge        float64
	Spend           float64
	Refund          float64
	Withdraw        float64
	PlatformRevenue float64
}

// AdminInventoryPort ranks listed projects by available inventory (ascending —
// low stock first). Backed by alloc's per-project inventory in the composition
// root, so the dashboard context need not depend on alloc.
type AdminInventoryPort interface {
	ProjectInventoryRanking(ctx context.Context, limit int) ([]AdminInventoryItem, error)
}

type AdminInventoryItem struct {
	Name      string
	Available int
}

// AdminView is the raw global aggregate read port implemented by infra.
type AdminView interface {
	OrderTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]CountBucket, error)
	CodeOrderTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]TypeCountBucket, error)
	CodeReceiptTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]TypeReceiptBucket, error)
	NewUserTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]CountBucket, error)
	ActiveUserTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]CountBucket, error)
	UsersCreatedBefore(ctx context.Context, before time.Time) (int, error)
	InventorySnapshot(ctx context.Context) (InventorySnapshot, error)
	ProjectCodeRanking(ctx context.Context, from, to time.Time, limit int) ([]ProjectCountRow, error)
}

type CountBucket struct {
	Bucket string
	Count  int
}

type TypeCountBucket struct {
	Bucket      string
	ProductType string // microsoft | domain
	Count       int
}

type TypeReceiptBucket struct {
	Bucket      string
	ProductType string
	Received    int
	AvgSeconds  int
}

type InventorySnapshot struct {
	MicrosoftTotal     int
	MicrosoftAvailable int
	DomainTotal        int
	DomainAvailable    int
}

// ---- assembled read model ------------------------------------------------

type AdminStats struct {
	RechargeAmount                     float64
	SpendAmount                        float64
	RefundAmount                       float64
	WithdrawAmount                     float64
	PlatformRevenue                    float64
	TotalOrders                        int
	SuccessfulCodeReceipts             int
	TotalUsers                         int
	ActiveUsers                        int
	NewUsers                           int
	MicrosoftTotalEmails               int
	MicrosoftAvailableEmails           int
	MicrosoftCodeReceipts              int
	MicrosoftCodeSuccessRate           float64
	MicrosoftAverageCodeReceiptSeconds int
	DomainTotalMailboxes               int
	DomainAvailableMailboxes           int
	DomainCodeReceipts                 int
	DomainCodeSuccessRate              float64
	DomainAverageCodeReceiptSeconds    int
}

type AdminTrendPoint struct {
	Label                              string
	RechargeAmount                     float64
	SpendAmount                        float64
	RefundAmount                       float64
	WithdrawAmount                     float64
	PlatformRevenue                    float64
	Orders                             int
	SuccessfulCodeReceipts             int
	TotalUsers                         int
	ActiveUsers                        int
	NewUsers                           int
	MicrosoftTotalEmails               int
	MicrosoftAvailableEmails           int
	MicrosoftReceivedCodes             int
	MicrosoftCodeSuccessRate           float64
	MicrosoftAverageCodeReceiptSeconds int
	DomainTotalMailboxes               int
	DomainAvailableMailboxes           int
	DomainReceivedCodes                int
	DomainCodeSuccessRate              float64
	DomainAverageCodeReceiptSeconds    int
}

type AdminInventoryRankItem struct {
	Name      string
	Available int
	Rank      int
}

type AdminDashboard struct {
	Stats                   AdminStats
	Trend                   []AdminTrendPoint
	ProjectCodeRanking      []RankItem
	ProjectInventoryRanking []AdminInventoryRankItem
}

// AdminQueryService builds the platform dashboard.
type AdminQueryService struct {
	view      AdminView
	finance   AdminFinancePort
	inventory AdminInventoryPort
	now       func() time.Time
}

func NewAdminQueryService(view AdminView, finance AdminFinancePort, inventory AdminInventoryPort) *AdminQueryService {
	return &AdminQueryService{view: view, finance: finance, inventory: inventory, now: time.Now}
}

const adminRankingLimit = 10

func (s *AdminQueryService) AdminDashboard(ctx context.Context, from, to *time.Time) (*AdminDashboard, error) {
	now := s.now()
	fromT, toT := resolveRange(from, to, now)
	gran := granularity(fromT, toT)
	sqlFmt := sqlFormat(gran)
	layout := bucketLayout(gran)
	sameYear := fromT.In(time.Local).Year() == toT.In(time.Local).Year()

	finance, err := s.finance.FinanceSummary(ctx, &fromT, &toT)
	if err != nil {
		return nil, err
	}
	orderRows, err := s.view.OrderTrend(ctx, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	codeOrderRows, err := s.view.CodeOrderTrend(ctx, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	receiptRows, err := s.view.CodeReceiptTrend(ctx, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	newUserRows, err := s.view.NewUserTrend(ctx, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	activeUserRows, err := s.view.ActiveUserTrend(ctx, sqlFmt, fromT, toT)
	if err != nil {
		return nil, err
	}
	baseUsers, err := s.view.UsersCreatedBefore(ctx, fromT)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.view.InventorySnapshot(ctx)
	if err != nil {
		return nil, err
	}
	codeRanking, err := s.view.ProjectCodeRanking(ctx, fromT, toT, adminRankingLimit)
	if err != nil {
		return nil, err
	}
	inventoryRanking, err := s.inventory.ProjectInventoryRanking(ctx, adminRankingLimit)
	if err != nil {
		return nil, err
	}

	financeByLabel := make(map[string]AdminFinanceBucket, len(finance.Trend))
	for _, b := range finance.Trend {
		financeByLabel[b.Label] = b
	}
	orderByKey := countByBucket(orderRows)
	newUserByKey := countByBucket(newUserRows)
	activeUserByKey := countByBucket(activeUserRows)
	msCodeOrders, domainCodeOrders := typeCountByBucket(codeOrderRows)
	msReceipts, domainReceipts := typeReceiptByBucket(receiptRows)

	trend := make([]AdminTrendPoint, 0, len(orderRows)+len(receiptRows))
	var totalOrders, totalReceipts int
	var msRecvTotal, msOrdersTotal, domainRecvTotal, domainOrdersTotal int
	var msSecondsWeighted, domainSecondsWeighted int
	var newUsersTotal, activeUsersTotal int
	cumulativeUsers := baseUsers
	for t := bucketStart(fromT, gran); !t.After(bucketStart(toT, gran)) && len(trend) < maxTrendBuckets; t = nextBucket(t, gran) {
		key := t.Format(layout)
		label := trendLabel(t, gran, sameYear)
		f := financeByLabel[label]
		msR := msReceipts[key]
		dR := domainReceipts[key]
		msCO := msCodeOrders[key]
		dCO := domainCodeOrders[key]
		newUsers := newUserByKey[key]
		activeUsers := activeUserByKey[key]
		cumulativeUsers += newUsers

		trend = append(trend, AdminTrendPoint{
			Label:                              label,
			RechargeAmount:                     f.Recharge,
			SpendAmount:                        f.Spend,
			RefundAmount:                       f.Refund,
			WithdrawAmount:                     f.Withdraw,
			PlatformRevenue:                    f.PlatformRevenue,
			Orders:                             orderByKey[key],
			SuccessfulCodeReceipts:             msR.Received + dR.Received,
			TotalUsers:                         cumulativeUsers,
			ActiveUsers:                        activeUsers,
			NewUsers:                           newUsers,
			MicrosoftTotalEmails:               snapshot.MicrosoftTotal,
			MicrosoftAvailableEmails:           snapshot.MicrosoftAvailable,
			MicrosoftReceivedCodes:             msR.Received,
			MicrosoftCodeSuccessRate:           round1(minFloat(100, pct(msR.Received, msCO))),
			MicrosoftAverageCodeReceiptSeconds: msR.AvgSeconds,
			DomainTotalMailboxes:               snapshot.DomainTotal,
			DomainAvailableMailboxes:           snapshot.DomainAvailable,
			DomainReceivedCodes:                dR.Received,
			DomainCodeSuccessRate:              round1(minFloat(100, pct(dR.Received, dCO))),
			DomainAverageCodeReceiptSeconds:    dR.AvgSeconds,
		})

		totalOrders += orderByKey[key]
		totalReceipts += msR.Received + dR.Received
		msRecvTotal += msR.Received
		domainRecvTotal += dR.Received
		msOrdersTotal += msCO
		domainOrdersTotal += dCO
		msSecondsWeighted += msR.AvgSeconds * msR.Received
		domainSecondsWeighted += dR.AvgSeconds * dR.Received
		// Accumulated in the loop (not sumCounts over all rows) so every header
		// stat reflects the same visited buckets and stays internally consistent
		// even if the trend loop ever caps.
		newUsersTotal += newUsers
		activeUsersTotal += activeUsers
	}
	if trend == nil {
		trend = []AdminTrendPoint{}
	}

	return &AdminDashboard{
		Stats: AdminStats{
			RechargeAmount:                     finance.RechargeAmount,
			SpendAmount:                        finance.SpendAmount,
			RefundAmount:                       finance.RefundAmount,
			WithdrawAmount:                     finance.WithdrawAmount,
			PlatformRevenue:                    finance.PlatformRevenue,
			TotalOrders:                        totalOrders,
			SuccessfulCodeReceipts:             totalReceipts,
			TotalUsers:                         cumulativeUsers,
			ActiveUsers:                        activeUsersTotal,
			NewUsers:                           newUsersTotal,
			MicrosoftTotalEmails:               snapshot.MicrosoftTotal,
			MicrosoftAvailableEmails:           snapshot.MicrosoftAvailable,
			MicrosoftCodeReceipts:              msRecvTotal,
			MicrosoftCodeSuccessRate:           round1(minFloat(100, pct(msRecvTotal, msOrdersTotal))),
			MicrosoftAverageCodeReceiptSeconds: weightedAvg(msSecondsWeighted, msRecvTotal),
			DomainTotalMailboxes:               snapshot.DomainTotal,
			DomainAvailableMailboxes:           snapshot.DomainAvailable,
			DomainCodeReceipts:                 domainRecvTotal,
			DomainCodeSuccessRate:              round1(minFloat(100, pct(domainRecvTotal, domainOrdersTotal))),
			DomainAverageCodeReceiptSeconds:    weightedAvg(domainSecondsWeighted, domainRecvTotal),
		},
		Trend:                   trend,
		ProjectCodeRanking:      adminRankItems(codeRanking),
		ProjectInventoryRanking: inventoryRankItems(inventoryRanking),
	}, nil
}

func adminRankItems(rows []ProjectCountRow) []RankItem {
	out := make([]RankItem, len(rows))
	for i, p := range rows {
		out[i] = RankItem{Name: projectLabel(p), Count: p.Count, Rank: i + 1}
	}
	return out
}

func inventoryRankItems(items []AdminInventoryItem) []AdminInventoryRankItem {
	out := make([]AdminInventoryRankItem, len(items))
	for i, it := range items {
		out[i] = AdminInventoryRankItem{Name: it.Name, Available: it.Available, Rank: i + 1}
	}
	return out
}

func countByBucket(rows []CountBucket) map[string]int {
	m := make(map[string]int, len(rows))
	for _, r := range rows {
		m[r.Bucket] = r.Count
	}
	return m
}

func typeCountByBucket(rows []TypeCountBucket) (microsoft, domain map[string]int) {
	microsoft = map[string]int{}
	domain = map[string]int{}
	for _, r := range rows {
		if r.ProductType == "domain" {
			domain[r.Bucket] = r.Count
		} else {
			microsoft[r.Bucket] = r.Count
		}
	}
	return microsoft, domain
}

func typeReceiptByBucket(rows []TypeReceiptBucket) (microsoft, domain map[string]TypeReceiptBucket) {
	microsoft = map[string]TypeReceiptBucket{}
	domain = map[string]TypeReceiptBucket{}
	for _, r := range rows {
		if r.ProductType == "domain" {
			domain[r.Bucket] = r
		} else {
			microsoft[r.Bucket] = r
		}
	}
	return microsoft, domain
}

func weightedAvg(weightedSum, weight int) int {
	if weight <= 0 {
		return 0
	}
	return weightedSum / weight
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
