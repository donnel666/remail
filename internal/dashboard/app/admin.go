package app

import (
	"context"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

// Admin platform dashboard. Reuses the bucketing/label helpers in service.go.
// Money comes from billing (AdminFinancePort) and per-project inventory from
// alloc (AdminInventoryPort); everything else is raw global aggregates via
// AdminView. Field names mirror admin-dashboard-mock.ts (consumed fields only).

// ---- ports ---------------------------------------------------------------

// AdminFinancePort combines billing's finance summary with the current
// supplier withdrawable balance.
type AdminFinancePort interface {
	FinanceSummary(ctx context.Context, from, to *time.Time) (AdminFinance, error)
}

type AdminFinance struct {
	RechargeAmount     float64
	SpendAmount        float64
	RefundAmount       float64
	WithdrawAmount     float64
	WithdrawableAmount float64
	PlatformRevenue    float64
	Trend              []AdminFinanceBucket
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
	PurchaseSummaries(ctx context.Context, from, to time.Time) ([]TypePurchaseSummary, error)
	NewUserTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]CountBucket, error)
	ActiveUserTrend(ctx context.Context, sqlFormat string, from, to time.Time) ([]CountBucket, error)
	TotalUsers(ctx context.Context) (int, error)
	InventorySnapshot(ctx context.Context) (InventorySnapshot, error)
	ProjectCodeRanking(ctx context.Context, from, to time.Time, limit int) ([]ProjectCountRow, error)
}

type CountBucket struct {
	Bucket string
	Count  int
}

type TypeCountBucket struct {
	Bucket      string
	ProductType string
	Count       int
}

type TypeReceiptBucket struct {
	Bucket       string
	ProductType  string
	Received     int
	AvgSeconds   int
	TotalSeconds int64
	Timed        int
}

type productFulfillmentTotals struct {
	CodeOrders   int
	Received     int
	TotalSeconds int64
	Timed        int
}

type PurchaseSummary struct {
	Orders       int
	Activated    int
	TotalSeconds int64
	Timed        int
}

type TypePurchaseSummary struct {
	ProductType string
	PurchaseSummary
}

type InventorySnapshot struct {
	MicrosoftTotal               int
	MicrosoftAvailable           int
	MicrosoftEnterpriseTotal     int
	MicrosoftEnterpriseAvailable int
	DomainTotal                  int
	DomainAvailable              int
	GmailTotal                   int
	GmailAvailable               int
	ICloudTotal                  int
	ICloudAvailable              int
	ProtoTotal                   int
	DaumTotal                    int
	ProtoAvailable               int
	DaumAvailable                int
}

// ---- assembled read model ------------------------------------------------

type AdminStats struct {
	RechargeAmount                                      float64
	SpendAmount                                         float64
	RefundAmount                                        float64
	WithdrawAmount                                      float64
	WithdrawableAmount                                  float64
	PlatformRevenue                                     float64
	TotalOrders                                         int
	SuccessfulCodeReceipts                              int
	TotalUsers                                          int
	ActiveUsers                                         int
	NewUsers                                            int
	MicrosoftTotalEmails                                int
	MicrosoftAvailableEmails                            int
	MicrosoftCodeReceipts                               int
	MicrosoftCodeSuccessRate                            float64
	MicrosoftAverageCodeReceiptSeconds                  int
	MicrosoftPurchaseActivations                        int
	MicrosoftPurchaseActivationSuccessRate              float64
	MicrosoftAveragePurchaseActivationSeconds           int
	MicrosoftEnterpriseTotalEmails                      int
	MicrosoftEnterpriseAvailableEmails                  int
	MicrosoftEnterpriseCodeReceipts                     int
	MicrosoftEnterpriseCodeSuccessRate                  float64
	MicrosoftEnterpriseAverageCodeReceiptSeconds        int
	MicrosoftEnterprisePurchaseActivations              int
	MicrosoftEnterprisePurchaseActivationSuccessRate    float64
	MicrosoftEnterpriseAveragePurchaseActivationSeconds int
	GmailTotalEmails                                    int
	GmailAvailableEmails                                int
	GmailCodeReceipts                                   int
	GmailCodeSuccessRate                                float64
	GmailAverageCodeReceiptSeconds                      int
	GmailPurchaseActivations                            int
	GmailPurchaseActivationSuccessRate                  float64
	GmailAveragePurchaseActivationSeconds               int
	GmailVariantTotalEmails                             int
	GmailVariantAvailableEmails                         int
	GmailVariantCodeReceipts                            int
	GmailVariantCodeSuccessRate                         float64
	GmailVariantAverageCodeReceiptSeconds               int
	GmailVariantPurchaseActivations                     int
	GmailVariantPurchaseActivationSuccessRate           float64
	GmailVariantAveragePurchaseActivationSeconds        int
	ICloudTotalEmails                                   int
	ICloudAvailableEmails                               int
	ICloudCodeReceipts                                  int
	ICloudCodeSuccessRate                               float64
	ICloudAverageCodeReceiptSeconds                     int
	ICloudPurchaseActivations                           int
	ICloudPurchaseActivationSuccessRate                 float64
	ICloudAveragePurchaseActivationSeconds              int
	ProtoTotalEmails                                    int
	DaumTotalEmails                                     int
	ProtoAvailableEmails                                int
	DaumAvailableEmails                                 int
	ProtoCodeReceipts                                   int
	DaumCodeReceipts                                    int
	ProtoCodeSuccessRate                                float64
	DaumCodeSuccessRate                                 float64
	ProtoAverageCodeReceiptSeconds                      int
	DaumAverageCodeReceiptSeconds                       int
	ProtoPurchaseActivations                            int
	DaumPurchaseActivations                             int
	ProtoPurchaseActivationSuccessRate                  float64
	DaumPurchaseActivationSuccessRate                   float64
	ProtoAveragePurchaseActivationSeconds               int
	DaumAveragePurchaseActivationSeconds                int
	DomainTotalMailboxes                                int
	DomainAvailableMailboxes                            int
	DomainCodeReceipts                                  int
	DomainCodeSuccessRate                               float64
	DomainAverageCodeReceiptSeconds                     int
}

type AdminTrendPoint struct {
	Label                                        string
	RechargeAmount                               float64
	SpendAmount                                  float64
	RefundAmount                                 float64
	WithdrawAmount                               float64
	PlatformRevenue                              float64
	Orders                                       int
	SuccessfulCodeReceipts                       int
	TotalUsers                                   int
	ActiveUsers                                  int
	NewUsers                                     int
	MicrosoftTotalEmails                         int
	MicrosoftAvailableEmails                     int
	MicrosoftReceivedCodes                       int
	MicrosoftCodeSuccessRate                     float64
	MicrosoftAverageCodeReceiptSeconds           int
	MicrosoftEnterpriseTotalEmails               int
	MicrosoftEnterpriseAvailableEmails           int
	MicrosoftEnterpriseReceivedCodes             int
	MicrosoftEnterpriseCodeSuccessRate           float64
	MicrosoftEnterpriseAverageCodeReceiptSeconds int
	GmailTotalEmails                             int
	GmailAvailableEmails                         int
	GmailReceivedCodes                           int
	GmailCodeSuccessRate                         float64
	GmailAverageCodeReceiptSeconds               int
	GmailVariantTotalEmails                      int
	GmailVariantAvailableEmails                  int
	GmailVariantReceivedCodes                    int
	GmailVariantCodeSuccessRate                  float64
	GmailVariantAverageCodeReceiptSeconds        int
	ICloudTotalEmails                            int
	ICloudAvailableEmails                        int
	ICloudReceivedCodes                          int
	ICloudCodeSuccessRate                        float64
	ICloudAverageCodeReceiptSeconds              int
	ProtoTotalEmails                             int
	DaumTotalEmails                              int
	ProtoAvailableEmails                         int
	DaumAvailableEmails                          int
	ProtoReceivedCodes                           int
	DaumReceivedCodes                            int
	ProtoCodeSuccessRate                         float64
	DaumCodeSuccessRate                          float64
	ProtoAverageCodeReceiptSeconds               int
	DaumAverageCodeReceiptSeconds                int
	DomainTotalMailboxes                         int
	DomainAvailableMailboxes                     int
	DomainReceivedCodes                          int
	DomainCodeSuccessRate                        float64
	DomainAverageCodeReceiptSeconds              int
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

func adminRankingLimitValue() int {
	return min(runtimeconfig.Int("admin_ranking_limit", adminRankingLimit, 1), 100)
}

func (s *AdminQueryService) AdminDashboard(ctx context.Context, from, to *time.Time) (*AdminDashboard, error) {
	now := s.now()
	fromT, toT := resolveRange(from, to, now)
	gran := granularity(fromT, toT)
	sqlFmt := sqlFormat(gran)
	layout := bucketLayout(gran)
	sameYear := fromT.In(dashboardLocation).Year() == toT.In(dashboardLocation).Year()

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
	purchaseRows, err := s.view.PurchaseSummaries(ctx, fromT, toT)
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
	totalUsers, err := s.view.TotalUsers(ctx)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.view.InventorySnapshot(ctx)
	if err != nil {
		return nil, err
	}
	rankingLimit := adminRankingLimitValue()
	codeRanking, err := s.view.ProjectCodeRanking(ctx, fromT, toT, rankingLimit)
	if err != nil {
		return nil, err
	}
	inventoryRanking, err := s.inventory.ProjectInventoryRanking(ctx, rankingLimit)
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
	codeOrdersByType := typeCountByBucket(codeOrderRows)
	receiptsByType := typeReceiptByBucket(receiptRows)
	purchasesByType := purchaseSummaryByType(purchaseRows)

	trend := make([]AdminTrendPoint, 0, len(orderRows)+len(receiptRows))
	var totalOrders, totalReceipts int
	fulfillmentTotals := map[string]productFulfillmentTotals{}
	var newUsersTotal, activeUsersTotal int
	for t := bucketStart(fromT, gran); !t.After(bucketStart(toT, gran)) && len(trend) < maxTrendBuckets; t = nextBucket(t, gran) {
		key := t.Format(layout)
		label := trendLabel(t, gran, sameYear)
		f := financeByLabel[label]
		msR := receiptsByType[productBucket{key, "microsoft"}]
		enterpriseR := receiptsByType[productBucket{key, "microsoft_enterprise"}]
		dR := receiptsByType[productBucket{key, "domain"}]
		gmailR := receiptsByType[productBucket{key, "gmail"}]
		gmailVariantR := receiptsByType[productBucket{key, "gmail_variant"}]
		iCloudR := receiptsByType[productBucket{key, "icloud"}]
		protoR := receiptsByType[productBucket{key, "proto"}]
		daumR := receiptsByType[productBucket{key, "daum"}]
		msCO := codeOrdersByType[productBucket{key, "microsoft"}]
		enterpriseCO := codeOrdersByType[productBucket{key, "microsoft_enterprise"}]
		dCO := codeOrdersByType[productBucket{key, "domain"}]
		gmailCO := codeOrdersByType[productBucket{key, "gmail"}]
		gmailVariantCO := codeOrdersByType[productBucket{key, "gmail_variant"}]
		iCloudCO := codeOrdersByType[productBucket{key, "icloud"}]
		protoCO := codeOrdersByType[productBucket{key, "proto"}]
		daumCO := codeOrdersByType[productBucket{key, "daum"}]
		newUsers := newUserByKey[key]
		activeUsers := activeUserByKey[key]
		bucketReceipts := 0
		for _, productType := range dashboardProductTypes {
			receipt := receiptsByType[productBucket{key, productType}]
			total := fulfillmentTotals[productType]
			total.Received += receipt.Received
			total.TotalSeconds += receipt.TotalSeconds
			total.Timed += receipt.Timed
			total.CodeOrders += codeOrdersByType[productBucket{key, productType}]
			fulfillmentTotals[productType] = total
			bucketReceipts += receipt.Received
		}
		trend = append(trend, AdminTrendPoint{
			Label:                              label,
			RechargeAmount:                     f.Recharge,
			SpendAmount:                        f.Spend,
			RefundAmount:                       f.Refund,
			WithdrawAmount:                     f.Withdraw,
			PlatformRevenue:                    f.PlatformRevenue,
			Orders:                             orderByKey[key],
			SuccessfulCodeReceipts:             bucketReceipts,
			TotalUsers:                         totalUsers,
			ActiveUsers:                        activeUsers,
			NewUsers:                           newUsers,
			MicrosoftTotalEmails:               snapshot.MicrosoftTotal,
			MicrosoftAvailableEmails:           snapshot.MicrosoftAvailable,
			MicrosoftReceivedCodes:             msR.Received,
			MicrosoftCodeSuccessRate:           round1(minFloat(100, pct(msR.Received, msCO))),
			MicrosoftAverageCodeReceiptSeconds: msR.AvgSeconds,
			MicrosoftEnterpriseTotalEmails:     snapshot.MicrosoftEnterpriseTotal,
			MicrosoftEnterpriseAvailableEmails: snapshot.MicrosoftEnterpriseAvailable,
			MicrosoftEnterpriseReceivedCodes:   enterpriseR.Received,
			MicrosoftEnterpriseCodeSuccessRate: round1(minFloat(100, pct(enterpriseR.Received, enterpriseCO))),
			MicrosoftEnterpriseAverageCodeReceiptSeconds: enterpriseR.AvgSeconds,
			GmailTotalEmails:                      snapshot.GmailTotal,
			GmailAvailableEmails:                  snapshot.GmailAvailable,
			GmailReceivedCodes:                    gmailR.Received,
			GmailCodeSuccessRate:                  round1(minFloat(100, pct(gmailR.Received, gmailCO))),
			GmailAverageCodeReceiptSeconds:        gmailR.AvgSeconds,
			GmailVariantTotalEmails:               snapshot.GmailTotal,
			GmailVariantAvailableEmails:           snapshot.GmailAvailable,
			GmailVariantReceivedCodes:             gmailVariantR.Received,
			GmailVariantCodeSuccessRate:           round1(minFloat(100, pct(gmailVariantR.Received, gmailVariantCO))),
			GmailVariantAverageCodeReceiptSeconds: gmailVariantR.AvgSeconds,
			ICloudTotalEmails:                     snapshot.ICloudTotal,
			ICloudAvailableEmails:                 snapshot.ICloudAvailable,
			ICloudReceivedCodes:                   iCloudR.Received,
			ICloudCodeSuccessRate:                 round1(minFloat(100, pct(iCloudR.Received, iCloudCO))),
			ICloudAverageCodeReceiptSeconds:       iCloudR.AvgSeconds,
			ProtoTotalEmails:                      snapshot.ProtoTotal,
			DaumTotalEmails:                       snapshot.DaumTotal,
			ProtoAvailableEmails:                  snapshot.ProtoAvailable,
			DaumAvailableEmails:                   snapshot.DaumAvailable,
			ProtoReceivedCodes:                    protoR.Received,
			DaumReceivedCodes:                     daumR.Received,
			ProtoCodeSuccessRate:                  round1(minFloat(100, pct(protoR.Received, protoCO))),
			DaumCodeSuccessRate:                   round1(minFloat(100, pct(daumR.Received, daumCO))),
			ProtoAverageCodeReceiptSeconds:        protoR.AvgSeconds,
			DaumAverageCodeReceiptSeconds:         daumR.AvgSeconds,
			DomainTotalMailboxes:                  snapshot.DomainTotal,
			DomainAvailableMailboxes:              snapshot.DomainAvailable,
			DomainReceivedCodes:                   dR.Received,
			DomainCodeSuccessRate:                 round1(minFloat(100, pct(dR.Received, dCO))),
			DomainAverageCodeReceiptSeconds:       dR.AvgSeconds,
		})

		totalOrders += orderByKey[key]
		totalReceipts += bucketReceipts
		// Accumulated in the loop (not sumCounts over all rows) so every header
		// stat reflects the same visited buckets and stays internally consistent
		// even if the trend loop ever caps.
		newUsersTotal += newUsers
		activeUsersTotal += activeUsers
	}
	if trend == nil {
		trend = []AdminTrendPoint{}
	}
	ms := fulfillmentTotals["microsoft"]
	enterprise := fulfillmentTotals["microsoft_enterprise"]
	domain := fulfillmentTotals["domain"]
	gmail := fulfillmentTotals["gmail"]
	gmailVariant := fulfillmentTotals["gmail_variant"]
	iCloud := fulfillmentTotals["icloud"]
	proto := fulfillmentTotals["proto"]
	daum := fulfillmentTotals["daum"]
	msPurchase := purchasesByType["microsoft"]
	enterprisePurchase := purchasesByType["microsoft_enterprise"]
	gmailPurchase := purchasesByType["gmail"]
	gmailVariantPurchase := purchasesByType["gmail_variant"]
	iCloudPurchase := purchasesByType["icloud"]
	protoPurchase := purchasesByType["proto"]
	daumPurchase := purchasesByType["daum"]

	return &AdminDashboard{
		Stats: AdminStats{
			RechargeAmount:                                      finance.RechargeAmount,
			SpendAmount:                                         finance.SpendAmount,
			RefundAmount:                                        finance.RefundAmount,
			WithdrawAmount:                                      finance.WithdrawAmount,
			WithdrawableAmount:                                  finance.WithdrawableAmount,
			PlatformRevenue:                                     finance.PlatformRevenue,
			TotalOrders:                                         totalOrders,
			SuccessfulCodeReceipts:                              totalReceipts,
			TotalUsers:                                          totalUsers,
			ActiveUsers:                                         activeUsersTotal,
			NewUsers:                                            newUsersTotal,
			MicrosoftTotalEmails:                                snapshot.MicrosoftTotal,
			MicrosoftAvailableEmails:                            snapshot.MicrosoftAvailable,
			MicrosoftCodeReceipts:                               ms.Received,
			MicrosoftCodeSuccessRate:                            round1(minFloat(100, pct(ms.Received, ms.CodeOrders))),
			MicrosoftAverageCodeReceiptSeconds:                  averageSeconds(ms.TotalSeconds, ms.Timed),
			MicrosoftPurchaseActivations:                        msPurchase.Activated,
			MicrosoftPurchaseActivationSuccessRate:              round1(minFloat(100, pct(msPurchase.Activated, msPurchase.Orders))),
			MicrosoftAveragePurchaseActivationSeconds:           averageSeconds(msPurchase.TotalSeconds, msPurchase.Timed),
			MicrosoftEnterpriseTotalEmails:                      snapshot.MicrosoftEnterpriseTotal,
			MicrosoftEnterpriseAvailableEmails:                  snapshot.MicrosoftEnterpriseAvailable,
			MicrosoftEnterpriseCodeReceipts:                     enterprise.Received,
			MicrosoftEnterpriseCodeSuccessRate:                  round1(minFloat(100, pct(enterprise.Received, enterprise.CodeOrders))),
			MicrosoftEnterpriseAverageCodeReceiptSeconds:        averageSeconds(enterprise.TotalSeconds, enterprise.Timed),
			MicrosoftEnterprisePurchaseActivations:              enterprisePurchase.Activated,
			MicrosoftEnterprisePurchaseActivationSuccessRate:    round1(minFloat(100, pct(enterprisePurchase.Activated, enterprisePurchase.Orders))),
			MicrosoftEnterpriseAveragePurchaseActivationSeconds: averageSeconds(enterprisePurchase.TotalSeconds, enterprisePurchase.Timed),
			GmailTotalEmails:                                    snapshot.GmailTotal,
			GmailAvailableEmails:                                snapshot.GmailAvailable,
			GmailCodeReceipts:                                   gmail.Received,
			GmailCodeSuccessRate:                                round1(minFloat(100, pct(gmail.Received, gmail.CodeOrders))),
			GmailAverageCodeReceiptSeconds:                      averageSeconds(gmail.TotalSeconds, gmail.Timed),
			GmailPurchaseActivations:                            gmailPurchase.Activated,
			GmailPurchaseActivationSuccessRate:                  round1(minFloat(100, pct(gmailPurchase.Activated, gmailPurchase.Orders))),
			GmailAveragePurchaseActivationSeconds:               averageSeconds(gmailPurchase.TotalSeconds, gmailPurchase.Timed),
			GmailVariantTotalEmails:                             snapshot.GmailTotal,
			GmailVariantAvailableEmails:                         snapshot.GmailAvailable,
			GmailVariantCodeReceipts:                            gmailVariant.Received,
			GmailVariantCodeSuccessRate:                         round1(minFloat(100, pct(gmailVariant.Received, gmailVariant.CodeOrders))),
			GmailVariantAverageCodeReceiptSeconds:               averageSeconds(gmailVariant.TotalSeconds, gmailVariant.Timed),
			GmailVariantPurchaseActivations:                     gmailVariantPurchase.Activated,
			GmailVariantPurchaseActivationSuccessRate:           round1(minFloat(100, pct(gmailVariantPurchase.Activated, gmailVariantPurchase.Orders))),
			GmailVariantAveragePurchaseActivationSeconds:        averageSeconds(gmailVariantPurchase.TotalSeconds, gmailVariantPurchase.Timed),
			ICloudTotalEmails:                                   snapshot.ICloudTotal,
			ICloudAvailableEmails:                               snapshot.ICloudAvailable,
			ICloudCodeReceipts:                                  iCloud.Received,
			ICloudCodeSuccessRate:                               round1(minFloat(100, pct(iCloud.Received, iCloud.CodeOrders))),
			ICloudAverageCodeReceiptSeconds:                     averageSeconds(iCloud.TotalSeconds, iCloud.Timed),
			ICloudPurchaseActivations:                           iCloudPurchase.Activated,
			ICloudPurchaseActivationSuccessRate:                 round1(minFloat(100, pct(iCloudPurchase.Activated, iCloudPurchase.Orders))),
			ICloudAveragePurchaseActivationSeconds:              averageSeconds(iCloudPurchase.TotalSeconds, iCloudPurchase.Timed),
			ProtoTotalEmails:                                    snapshot.ProtoTotal,
			DaumTotalEmails:                                     snapshot.DaumTotal,
			ProtoAvailableEmails:                                snapshot.ProtoAvailable,
			DaumAvailableEmails:                                 snapshot.DaumAvailable,
			ProtoCodeReceipts:                                   proto.Received,
			DaumCodeReceipts:                                    daum.Received,
			ProtoCodeSuccessRate:                                round1(minFloat(100, pct(proto.Received, proto.CodeOrders))),
			DaumCodeSuccessRate:                                 round1(minFloat(100, pct(daum.Received, daum.CodeOrders))),
			ProtoAverageCodeReceiptSeconds:                      averageSeconds(proto.TotalSeconds, proto.Timed),
			DaumAverageCodeReceiptSeconds:                       averageSeconds(daum.TotalSeconds, daum.Timed),
			ProtoPurchaseActivations:                            protoPurchase.Activated,
			DaumPurchaseActivations:                             daumPurchase.Activated,
			ProtoPurchaseActivationSuccessRate:                  round1(minFloat(100, pct(protoPurchase.Activated, protoPurchase.Orders))),
			DaumPurchaseActivationSuccessRate:                   round1(minFloat(100, pct(daumPurchase.Activated, daumPurchase.Orders))),
			ProtoAveragePurchaseActivationSeconds:               averageSeconds(protoPurchase.TotalSeconds, protoPurchase.Timed),
			DaumAveragePurchaseActivationSeconds:                averageSeconds(daumPurchase.TotalSeconds, daumPurchase.Timed),
			DomainTotalMailboxes:                                snapshot.DomainTotal,
			DomainAvailableMailboxes:                            snapshot.DomainAvailable,
			DomainCodeReceipts:                                  domain.Received,
			DomainCodeSuccessRate:                               round1(minFloat(100, pct(domain.Received, domain.CodeOrders))),
			DomainAverageCodeReceiptSeconds:                     averageSeconds(domain.TotalSeconds, domain.Timed),
		},
		Trend:                   trend,
		ProjectCodeRanking:      adminRankItems(codeRanking),
		ProjectInventoryRanking: inventoryRankItems(inventoryRanking),
	}, nil
}

func adminRankItems(rows []ProjectCountRow) []RankItem {
	out := make([]RankItem, len(rows))
	for i, p := range rows {
		out[i] = RankItem{Name: projectLabel(p.ProjectID, p.Name), Count: p.Count, Rank: i + 1}
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

var dashboardProductTypes = [...]string{"microsoft", "microsoft_enterprise", "domain", "gmail", "gmail_variant", "icloud", "proto", "daum"}

type productBucket struct {
	Bucket      string
	ProductType string
}

func typeCountByBucket(rows []TypeCountBucket) map[productBucket]int {
	result := make(map[productBucket]int, len(rows))
	for _, r := range rows {
		result[productBucket{r.Bucket, r.ProductType}] = r.Count
	}
	return result
}

func typeReceiptByBucket(rows []TypeReceiptBucket) map[productBucket]TypeReceiptBucket {
	result := make(map[productBucket]TypeReceiptBucket, len(rows))
	for _, r := range rows {
		result[productBucket{r.Bucket, r.ProductType}] = r
	}
	return result
}

func purchaseSummaryByType(rows []TypePurchaseSummary) map[string]PurchaseSummary {
	result := make(map[string]PurchaseSummary, len(rows))
	for _, row := range rows {
		result[row.ProductType] = row.PurchaseSummary
	}
	return result
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
