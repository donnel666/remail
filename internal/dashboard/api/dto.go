package api

import dashboardapp "github.com/donnel666/remail/internal/dashboard/app"

// The DTOs mirror the api/openapi.yaml Dashboard* schemas 1:1 (only the fields
// the console panels consume). Field names match the frontend interfaces so the
// presentation stays untouched.

type DashboardResponse struct {
	Stats                     DashboardStats           `json:"stats"`
	Trend                     []DashboardTrendPoint    `json:"trend"`
	ProjectSeries             []DashboardProjectSeries `json:"projectSeries"`
	ProjectCodeRanking        []DashboardRankItem      `json:"projectCodeRanking"`
	CodeRatio                 float64                  `json:"codeRatio"`
	PurchaseRatio             float64                  `json:"purchaseRatio"`
	TodayCodeRanking          []DashboardRankItem      `json:"todayCodeRanking"`
	HistoricalCodeRanking     []DashboardRankItem      `json:"historicalCodeRanking"`
	TodayCurrentUserRank      DashboardRankItem        `json:"todayCurrentUserRank"`
	HistoricalCurrentUserRank DashboardRankItem        `json:"historicalCurrentUserRank"`
}

type DashboardStats struct {
	WalletBalance             float64 `json:"walletBalance"`
	HistoricalSpend           float64 `json:"historicalSpend"`
	TodayOrders               int     `json:"todayOrders"`
	TotalOrders               int     `json:"totalOrders"`
	TodayCodeReceipts         int     `json:"todayCodeReceipts"`
	TotalCodeReceipts         int     `json:"totalCodeReceipts"`
	CodeSuccessRate           float64 `json:"codeSuccessRate"`
	AverageCodeReceiptSeconds int     `json:"averageCodeReceiptSeconds"`
}

type DashboardTrendPoint struct {
	Label                     string  `json:"label"`
	Orders                    int     `json:"orders"`
	CodeOrders                int     `json:"codeOrders"`
	ReceivedCodes             int     `json:"receivedCodes"`
	AverageCodeReceiptSeconds int     `json:"averageCodeReceiptSeconds"`
	Spend                     float64 `json:"spend"`
}

type DashboardProjectSeries struct {
	Name  string    `json:"name"`
	Spend []float64 `json:"spend"`
}

type DashboardRankItem struct {
	Name          string `json:"name"`
	Count         int    `json:"count"`
	Rank          int    `json:"rank"`
	IsCurrentUser bool   `json:"isCurrentUser,omitempty"`
}

func dashboardResponse(d *dashboardapp.ConsoleDashboard) DashboardResponse {
	trend := make([]DashboardTrendPoint, len(d.Trend))
	for i, p := range d.Trend {
		trend[i] = DashboardTrendPoint(p)
	}
	series := make([]DashboardProjectSeries, len(d.ProjectSeries))
	for i, s := range d.ProjectSeries {
		spend := s.Spend
		if spend == nil {
			spend = []float64{}
		}
		series[i] = DashboardProjectSeries{Name: s.Name, Spend: spend}
	}
	return DashboardResponse{
		Stats:                     DashboardStats(d.Stats),
		Trend:                     trend,
		ProjectSeries:             series,
		ProjectCodeRanking:        rankItems(d.ProjectCodeRanking),
		CodeRatio:                 d.CodeRatio,
		PurchaseRatio:             d.PurchaseRatio,
		TodayCodeRanking:          rankItems(d.TodayCodeRanking),
		HistoricalCodeRanking:     rankItems(d.HistoricalCodeRanking),
		TodayCurrentUserRank:      DashboardRankItem(d.TodayCurrentUserRank),
		HistoricalCurrentUserRank: DashboardRankItem(d.HistoricalCurrentUserRank),
	}
}

func rankItems(items []dashboardapp.RankItem) []DashboardRankItem {
	out := make([]DashboardRankItem, len(items))
	for i, it := range items {
		out[i] = DashboardRankItem(it)
	}
	return out
}
