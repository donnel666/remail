package smsbower

import (
	"context"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/money"
	"github.com/shopspring/decimal"
)

func (s *Service) ListInventory(ctx context.Context, projectIDs []uint) ([]InventoryItem, error) {
	projectIDs = uniqueUintValues(projectIDs)
	if len(projectIDs) == 0 {
		return []InventoryItem{}, nil
	}
	var rows []struct {
		ProjectID       uint       `gorm:"column:project_id"`
		ProductID       uint       `gorm:"column:product_id"`
		ProductStatus   string     `gorm:"column:product_status"`
		CodeEnabled     bool       `gorm:"column:code_enabled"`
		CodePrice       string     `gorm:"column:code_price"`
		ServiceCode     string     `gorm:"column:service_code"`
		UpstreamPrice   string     `gorm:"column:gmail_price"`
		Stock           uint       `gorm:"column:gmail_stock"`
		ServiceActive   bool       `gorm:"column:service_active"`
		Balance         string     `gorm:"column:balance"`
		HealthStatus    string     `gorm:"column:health_status"`
		LastSuccessAt   *time.Time `gorm:"column:last_success_at"`
		ProviderEnabled bool       `gorm:"column:provider_enabled"`
		Configured      bool       `gorm:"column:provider_configured"`
		SyncInterval    uint       `gorm:"column:sync_interval_minutes"`
		PointsPerUnit   string     `gorm:"column:points_per_unit"`
		MinMarginRate   string     `gorm:"column:min_margin_rate"`
	}
	if err := s.dbFor(ctx).Table("project_products AS pp").
		Select(`pp.project_id, pp.id AS product_id, pp.status AS product_status,
pp.code_enabled, pp.code_price, COALESCE(r.service_code, '') AS service_code,
COALESCE(svc.gmail_price, 0) AS gmail_price, COALESCE(svc.gmail_stock, 0) AS gmail_stock,
COALESCE(svc.active, 0) AS service_active, account.balance, account.health_status,
account.last_success_at, cfg.enabled AS provider_enabled,
(cfg.api_key <> '') AS provider_configured, cfg.sync_interval_minutes,
cfg.points_per_unit, cfg.min_margin_rate`).
		Joins("LEFT JOIN smsbower_project_routes AS r ON r.project_id = pp.project_id AND r.enabled = ?", true).
		Joins("LEFT JOIN smsbower_services AS svc ON svc.code = r.service_code").
		Joins("JOIN smsbower_account_state AS account ON account.id = 1").
		Joins("JOIN smsbower_config AS cfg ON cfg.id = 1").
		Where("pp.project_id IN ? AND pp.type = ?", projectIDs, "gmail").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load SMSBower inventory: %w", err)
	}
	minimumRatio := decimal.NewFromInt(1)
	var minimumRatioValue string
	if err := s.dbFor(ctx).Table("user_groups").Select("COALESCE(MIN(price_discount_ratio), 1)").Where("enabled = ?", true).Scan(&minimumRatioValue).Error; err != nil {
		return nil, fmt.Errorf("load minimum user price ratio: %w", err)
	}
	if parsed, err := money.Parse(minimumRatioValue); err == nil && !parsed.IsNegative() {
		minimumRatio = parsed
	}
	serviceReserved := make(map[string]uint)
	reservedBalance := decimal.Zero
	var snapshotAt *time.Time
	if len(rows) > 0 {
		snapshotAt = rows[0].LastSuccessAt
	}
	if snapshotAt != nil {
		var reserveRows []struct {
			ServiceCode string `gorm:"column:service_code"`
			Count       uint   `gorm:"column:reserved_count"`
			Cost        string `gorm:"column:reserved_cost"`
		}
		if err := s.dbFor(ctx).Model(&orderModel{}).
			Select("service_code, COUNT(*) AS reserved_count, COALESCE(SUM(upstream_price_snapshot), 0) AS reserved_cost").
			Where("status IN ? OR (created_at >= ? AND status IN ?)",
				[]string{StatusPending, StatusProvisioning, StatusUnknown}, snapshotAt.UTC(),
				[]string{StatusActive, StatusCompleting, StatusCompleted, StatusCancelling}).
			Group("service_code").Scan(&reserveRows).Error; err != nil {
			return nil, fmt.Errorf("load SMSBower inventory reservations: %w", err)
		}
		for _, row := range reserveRows {
			serviceReserved[row.ServiceCode] = row.Count
			reservedBalance = reservedBalance.Add(parseDecimalOrZero(row.Cost))
		}
	}
	items := make([]InventoryItem, 0, len(rows))
	for _, row := range rows {
		item := InventoryItem{ProjectID: row.ProjectID, ProductID: row.ProductID}
		health := supplyRow{
			ServiceCode: row.ServiceCode, Price: row.UpstreamPrice, Stock: row.Stock,
			ServiceActive: row.ServiceActive, Balance: row.Balance, HealthStatus: row.HealthStatus,
			LastSuccessAt: row.LastSuccessAt, Enabled: row.ProviderEnabled, Configured: row.Configured,
			SyncInterval: row.SyncInterval, PointsPerUnit: row.PointsPerUnit, MinMarginRate: row.MinMarginRate,
		}
		if row.ProductStatus != "enabled" || !row.CodeEnabled || row.ServiceCode == "" || !supplyRowHealthy(health, s.now()) {
			items = append(items, item)
			continue
		}
		price, points, minMargin, err := pricing(row.UpstreamPrice, row.PointsPerUnit, row.MinMarginRate)
		balance, balanceErr := money.Parse(row.Balance)
		sale, saleErr := money.Parse(row.CodePrice)
		if err != nil || balanceErr != nil || saleErr != nil {
			items = append(items, item)
			continue
		}
		if _, _, _, safe := calculateMargin(sale.Mul(minimumRatio), price, points, minMargin); !safe {
			items = append(items, item)
			continue
		}
		stock := uint(0)
		if row.Stock > serviceReserved[row.ServiceCode] {
			stock = row.Stock - serviceReserved[row.ServiceCode]
		}
		availableBalance := decimal.Max(balance.Sub(reservedBalance), decimal.Zero)
		item.CodeAvailable = int64(affordableStock(stock, availableBalance, price))
		items = append(items, item)
	}
	return items, nil
}

func uniqueUintValues(values []uint) []uint {
	result := make([]uint, 0, len(values))
	seen := make(map[uint]struct{}, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
