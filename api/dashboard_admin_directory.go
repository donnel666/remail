package api

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"gorm.io/gorm"
)

var (
	_ dashboardapp.AdminFinancePort   = dashboardFinanceDirectory{}
	_ dashboardapp.AdminInventoryPort = dashboardInventoryDirectory{}
)

// dashboardFinanceDirectory adapts billing's finance summary to the admin
// dashboard's finance port so the money numbers match /admin/finance/summary.
// It lives in the composition root so the dashboard context need not depend on
// billing (mirrors financeUserDirectory / orderOwnerDirectory).
type dashboardFinanceDirectory struct {
	wallet *billingapp.WalletUseCase
}

func (d dashboardFinanceDirectory) FinanceSummary(ctx context.Context, from, to *time.Time) (dashboardapp.AdminFinance, error) {
	res, err := d.wallet.FinanceSummary(ctx, from, to)
	if err != nil {
		return dashboardapp.AdminFinance{}, err
	}
	trend := make([]dashboardapp.AdminFinanceBucket, len(res.Trend))
	for i, p := range res.Trend {
		trend[i] = dashboardapp.AdminFinanceBucket{
			Label:           p.Label,
			Recharge:        p.Recharge,
			Spend:           p.Spend,
			Refund:          p.Refund,
			Withdraw:        p.Withdraw,
			PlatformRevenue: p.PlatformRevenue,
		}
	}
	return dashboardapp.AdminFinance{
		RechargeAmount:  dashboardMoneyFloat(res.RechargeAmount),
		SpendAmount:     dashboardMoneyFloat(res.SpendAmount),
		RefundAmount:    dashboardMoneyFloat(res.RefundAmount),
		WithdrawAmount:  dashboardMoneyFloat(res.WithdrawAmount),
		PlatformRevenue: dashboardMoneyFloat(res.PlatformRevenue),
		Trend:           trend,
	}, nil
}

func dashboardMoneyFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// dashboardInventoryDirectory ranks listed projects by public available
// inventory (ascending — low stock first) using alloc's per-project stats. The
// per-project call is TTL-cached in alloc; buyerUserID 0 = public scope.
type dashboardInventoryDirectory struct {
	db    *gorm.DB
	alloc *allocapp.UseCase
}

func (d dashboardInventoryDirectory) ProjectInventoryRanking(ctx context.Context, limit int) ([]dashboardapp.AdminInventoryItem, error) {
	var projects []struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := d.db.WithContext(ctx).
		Table("projects").
		Select("id, name").
		Where("status = 'listed'").
		Scan(&projects).Error; err != nil {
		return nil, err
	}
	items := make([]dashboardapp.AdminInventoryItem, 0, len(projects))
	for _, p := range projects {
		stats, err := d.alloc.GetInventoryStats(ctx, p.ID, 0)
		if errors.Is(err, allocdomain.ErrProjectNotAllocatable) {
			// No allocatable products for this project — legitimately absent from
			// the inventory ranking, not an error.
			continue
		}
		if err != nil {
			// Propagate genuine failures (DB timeout, cancellation) rather than
			// silently dropping projects — otherwise a transient error on the
			// lowest-stock projects would hide exactly what this alert surfaces.
			return nil, err
		}
		if stats == nil {
			continue
		}
		items = append(items, dashboardapp.AdminInventoryItem{Name: p.Name, Available: int(stats.TotalAvailable)})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Available < items[j].Available })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
