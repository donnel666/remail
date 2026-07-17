package api

import dashboardapp "github.com/donnel666/remail/internal/dashboard/app"

// Admin DTOs mirror the api/openapi.yaml AdminDashboard* schemas 1:1 (only the
// fields the admin panels consume). Field order matches the app structs so the
// stats/trend structs convert directly.

type AdminDashboardResponse struct {
	Stats                   AdminDashboardStats               `json:"stats"`
	Trend                   []AdminDashboardTrendPoint        `json:"trend"`
	ProjectCodeRanking      []AdminDashboardRankItem          `json:"projectCodeRanking"`
	ProjectInventoryRanking []AdminDashboardInventoryRankItem `json:"projectInventoryRanking"`
}

type AdminDashboardStats struct {
	RechargeAmount                     float64 `json:"rechargeAmount"`
	SpendAmount                        float64 `json:"spendAmount"`
	RefundAmount                       float64 `json:"refundAmount"`
	WithdrawAmount                     float64 `json:"withdrawAmount"`
	PlatformRevenue                    float64 `json:"platformRevenue"`
	TotalOrders                        int     `json:"totalOrders"`
	SuccessfulCodeReceipts             int     `json:"successfulCodeReceipts"`
	TotalUsers                         int     `json:"totalUsers"`
	ActiveUsers                        int     `json:"activeUsers"`
	NewUsers                           int     `json:"newUsers"`
	MicrosoftTotalEmails               int     `json:"microsoftTotalEmails"`
	MicrosoftAvailableEmails           int     `json:"microsoftAvailableEmails"`
	MicrosoftCodeReceipts              int     `json:"microsoftCodeReceipts"`
	MicrosoftCodeSuccessRate           float64 `json:"microsoftCodeSuccessRate"`
	MicrosoftAverageCodeReceiptSeconds int     `json:"microsoftAverageCodeReceiptSeconds"`
	DomainTotalMailboxes               int     `json:"domainTotalMailboxes"`
	DomainAvailableMailboxes           int     `json:"domainAvailableMailboxes"`
	DomainCodeReceipts                 int     `json:"domainCodeReceipts"`
	DomainCodeSuccessRate              float64 `json:"domainCodeSuccessRate"`
	DomainAverageCodeReceiptSeconds    int     `json:"domainAverageCodeReceiptSeconds"`
}

type AdminDashboardTrendPoint struct {
	Label                              string  `json:"label"`
	RechargeAmount                     float64 `json:"rechargeAmount"`
	SpendAmount                        float64 `json:"spendAmount"`
	RefundAmount                       float64 `json:"refundAmount"`
	WithdrawAmount                     float64 `json:"withdrawAmount"`
	PlatformRevenue                    float64 `json:"platformRevenue"`
	Orders                             int     `json:"orders"`
	SuccessfulCodeReceipts             int     `json:"successfulCodeReceipts"`
	TotalUsers                         int     `json:"totalUsers"`
	ActiveUsers                        int     `json:"activeUsers"`
	NewUsers                           int     `json:"newUsers"`
	MicrosoftTotalEmails               int     `json:"microsoftTotalEmails"`
	MicrosoftAvailableEmails           int     `json:"microsoftAvailableEmails"`
	MicrosoftReceivedCodes             int     `json:"microsoftReceivedCodes"`
	MicrosoftCodeSuccessRate           float64 `json:"microsoftCodeSuccessRate"`
	MicrosoftAverageCodeReceiptSeconds int     `json:"microsoftAverageCodeReceiptSeconds"`
	DomainTotalMailboxes               int     `json:"domainTotalMailboxes"`
	DomainAvailableMailboxes           int     `json:"domainAvailableMailboxes"`
	DomainReceivedCodes                int     `json:"domainReceivedCodes"`
	DomainCodeSuccessRate              float64 `json:"domainCodeSuccessRate"`
	DomainAverageCodeReceiptSeconds    int     `json:"domainAverageCodeReceiptSeconds"`
}

type AdminDashboardRankItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Rank  int    `json:"rank"`
}

type AdminDashboardInventoryRankItem struct {
	Name      string `json:"name"`
	Available int    `json:"available"`
	Rank      int    `json:"rank"`
}

func adminDashboardResponse(d *dashboardapp.AdminDashboard) AdminDashboardResponse {
	trend := make([]AdminDashboardTrendPoint, len(d.Trend))
	for i, p := range d.Trend {
		trend[i] = AdminDashboardTrendPoint(p)
	}
	codeRanking := make([]AdminDashboardRankItem, len(d.ProjectCodeRanking))
	for i, it := range d.ProjectCodeRanking {
		codeRanking[i] = AdminDashboardRankItem{Name: it.Name, Count: it.Count, Rank: it.Rank}
	}
	inventoryRanking := make([]AdminDashboardInventoryRankItem, len(d.ProjectInventoryRanking))
	for i, it := range d.ProjectInventoryRanking {
		inventoryRanking[i] = AdminDashboardInventoryRankItem{Name: it.Name, Available: it.Available, Rank: it.Rank}
	}
	return AdminDashboardResponse{
		Stats:                   AdminDashboardStats(d.Stats),
		Trend:                   trend,
		ProjectCodeRanking:      codeRanking,
		ProjectInventoryRanking: inventoryRanking,
	}
}
