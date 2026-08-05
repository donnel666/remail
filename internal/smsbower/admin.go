package smsbower

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) AccountStatus(ctx context.Context) (*AccountStatus, error) {
	var row struct {
		accountStateModel
		Enabled    bool `gorm:"column:provider_enabled"`
		Configured bool `gorm:"column:provider_configured"`
	}
	if err := s.dbFor(ctx).Table("smsbower_account_state AS account").
		Select("account.*, cfg.enabled AS provider_enabled, (cfg.api_key <> '') AS provider_configured").
		Joins("JOIN smsbower_config AS cfg ON cfg.id = account.id").
		Where("account.id = 1").Take(&row).Error; err != nil {
		return nil, fmt.Errorf("load SMSBower account status: %w", err)
	}
	healthStatus := row.HealthStatus
	if !row.Enabled {
		healthStatus = "disabled"
	}
	return &AccountStatus{
		Enabled: row.Enabled, Configured: row.Configured, Balance: row.Balance,
		HealthStatus: healthStatus, ConsecutiveFailures: row.ConsecutiveFailures,
		LastSafeError: row.LastSafeError, LastSyncedAt: row.LastSyncedAt, LastSuccessAt: row.LastSuccessAt,
	}, nil
}

func (s *Service) ListServices(ctx context.Context) ([]ServiceItem, error) {
	var models []serviceModel
	if err := s.dbFor(ctx).Order("active DESC, name ASC, code ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list SMSBower services: %w", err)
	}
	items := make([]ServiceItem, len(models))
	for i := range models {
		items[i] = ServiceItem{
			Code: models[i].Code, Name: models[i].Name, GmailPrice: models[i].GmailPrice,
			GmailStock: models[i].GmailStock, PreviousPrice: models[i].PreviousPrice,
			Active: models[i].Active, PriceChangedAt: models[i].PriceChangedAt, LastSeenAt: models[i].LastSeenAt,
		}
	}
	return items, nil
}

func (s *Service) PutMapping(ctx context.Context, projectID uint, serviceCode string, meta MutationMeta) error {
	serviceCode = strings.TrimSpace(serviceCode)
	if projectID == 0 || serviceCode == "" || len(serviceCode) > 64 {
		return ErrInvalidRoute
	}
	model := routeModel{ProjectID: projectID, ServiceCode: serviceCode, Enabled: true}
	return s.mutate(ctx, meta, &governancedomain.OperationLog{
		OperationType: "smsbower.mapping.put", ResourceType: "project", ResourceID: fmt.Sprintf("%d", projectID),
		Result: "success", SafeSummary: fmt.Sprintf("updated SMSBower project mapping project_id=%d", projectID),
	}, func(txCtx context.Context) error {
		db := s.dbFor(txCtx)
		var lockedProjectID uint
		if err := db.Table("projects").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ?", projectID).Take(&lockedProjectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidRoute
			}
			return fmt.Errorf("lock SMSBower mapping project: %w", err)
		}
		var serviceCount int64
		if err := db.Model(&serviceModel{}).Where("code = ?", serviceCode).Count(&serviceCount).Error; err != nil {
			return fmt.Errorf("check SMSBower service: %w", err)
		}
		if serviceCount == 0 {
			return ErrInvalidRoute
		}
		return db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "project_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"service_code": serviceCode, "enabled": true, "updated_at": s.now(),
			}),
		}).Create(&model).Error
	})
}

func (s *Service) DeleteMapping(ctx context.Context, projectID uint, meta MutationMeta) error {
	if projectID == 0 {
		return ErrInvalidRoute
	}
	return s.mutate(ctx, meta, &governancedomain.OperationLog{
		OperationType: "smsbower.mapping.delete", ResourceType: "project", ResourceID: fmt.Sprintf("%d", projectID),
		Result: "success", SafeSummary: fmt.Sprintf("deleted SMSBower project mapping project_id=%d", projectID),
	}, func(txCtx context.Context) error {
		return s.dbFor(txCtx).Where("project_id = ?", projectID).Delete(&routeModel{}).Error
	})
}

func (s *Service) ListMappings(ctx context.Context) ([]MappingItem, error) {
	var rows []struct {
		ProjectID     uint   `gorm:"column:project_id"`
		ProjectName   string `gorm:"column:project_name"`
		CodePrice     string `gorm:"column:code_price"`
		PurchasePrice string `gorm:"column:purchase_price"`
		ServiceCode   string `gorm:"column:service_code"`
		ServiceName   string `gorm:"column:service_name"`
		UpstreamPrice string `gorm:"column:gmail_price"`
		PointsPerUnit string `gorm:"column:points_per_unit"`
	}
	if err := s.dbFor(ctx).Table("smsbower_project_routes AS r").
		Select(`p.id AS project_id, p.name AS project_name,
COALESCE(pp.code_price, 0) AS code_price, COALESCE(pp.purchase_price, 0) AS purchase_price,
r.service_code, COALESCE(svc.name, '') AS service_name,
COALESCE(svc.gmail_price, 0) AS gmail_price, cfg.points_per_unit`).
		Joins("JOIN projects AS p ON p.id = r.project_id").
		Joins("LEFT JOIN project_products AS pp ON pp.project_id = p.id AND pp.type = ?", "gmail").
		Joins("LEFT JOIN smsbower_services AS svc ON svc.code = r.service_code").
		Joins("JOIN smsbower_config AS cfg ON cfg.id = 1").
		Order("p.name ASC, p.id ASC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list SMSBower mappings: %w", err)
	}
	items := make([]MappingItem, len(rows))
	for i, row := range rows {
		upstreamPrice := parseDecimalOrZero(row.UpstreamPrice)
		points := parseDecimalOrZero(row.PointsPerUnit)
		items[i] = MappingItem{
			ProjectID: row.ProjectID, ProjectName: row.ProjectName,
			ProviderServiceCode: row.ServiceCode, ProviderServiceName: row.ServiceName,
			UpstreamPrice: row.UpstreamPrice, CostPoints: money.Format(upstreamPrice.Mul(points)),
			CodePrice: row.CodePrice, PurchasePrice: row.PurchasePrice,
		}
	}
	return items, nil
}

func (s *Service) Finance(ctx context.Context) (*FinanceReport, error) {
	var row struct {
		OrderCount      int64  `gorm:"column:order_count"`
		ActivationCount int64  `gorm:"column:activation_count"`
		ZeroCodeCount   int64  `gorm:"column:zero_code_count"`
		OneCodeCount    int64  `gorm:"column:one_code_count"`
		TwoCodeCount    int64  `gorm:"column:two_code_count"`
		ThreeCodeCount  int64  `gorm:"column:three_code_count"`
		Sales           string `gorm:"column:sales"`
		Refunds         string `gorm:"column:refunds"`
		SettledCost     string `gorm:"column:settled_cost"`
		ReservedCost    string `gorm:"column:reserved_cost"`
		UnknownCost     string `gorm:"column:unknown_cost"`
	}
	if err := s.dbFor(ctx).Table("smsbower_orders AS s").
		Select(`COUNT(*) AS order_count,
COALESCE(SUM(CASE WHEN s.remote_mail_id IS NOT NULL THEN 1 ELSE 0 END), 0) AS activation_count,
COALESCE(SUM(CASE WHEN s.received_count = 0 THEN 1 ELSE 0 END), 0) AS zero_code_count,
COALESCE(SUM(CASE WHEN s.received_count = 1 THEN 1 ELSE 0 END), 0) AS one_code_count,
COALESCE(SUM(CASE WHEN s.received_count = 2 THEN 1 ELSE 0 END), 0) AS two_code_count,
COALESCE(SUM(CASE WHEN s.received_count = 3 THEN 1 ELSE 0 END), 0) AS three_code_count,
COALESCE(SUM(CASE WHEN o.debit_tx_id IS NOT NULL THEN o.pay_amount ELSE 0 END), 0) AS sales,
COALESCE(SUM(o.refund_amount), 0) AS refunds,
COALESCE(SUM(CASE WHEN s.status = 'completed' THEN s.cost_points_snapshot ELSE 0 END), 0) AS settled_cost,
COALESCE(SUM(CASE WHEN s.status IN ('pending','provisioning','active','completing','cancelling') THEN s.cost_points_snapshot ELSE 0 END), 0) AS reserved_cost,
COALESCE(SUM(CASE WHEN s.status = 'unknown' THEN s.cost_points_snapshot ELSE 0 END), 0) AS unknown_cost`).
		Joins("JOIN orders AS o ON o.order_no = s.order_no").Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("load SMSBower finance overview: %w", err)
	}
	sales := parseDecimalOrZero(row.Sales)
	refunds := parseDecimalOrZero(row.Refunds)
	settled := parseDecimalOrZero(row.SettledCost)
	reserved := parseDecimalOrZero(row.ReservedCost)
	unknown := parseDecimalOrZero(row.UnknownCost)
	netRevenue := sales.Sub(refunds)
	conservativeCost := settled.Add(reserved).Add(unknown)
	profit := netRevenue.Sub(conservativeCost)
	margin := decimal.Zero
	if netRevenue.IsPositive() {
		margin = profit.Div(netRevenue)
	}
	report := &FinanceReport{Overview: FinanceOverview{
		OrderCount: row.OrderCount, ActivationCount: row.ActivationCount,
		ZeroCodeCount: row.ZeroCodeCount, OneCodeCount: row.OneCodeCount,
		TwoCodeCount: row.TwoCodeCount, ThreeCodeCount: row.ThreeCodeCount,
		Sales: money.Format(sales), Refunds: money.Format(refunds), NetRevenue: money.Format(netRevenue),
		SettledCost: money.Format(settled), ReservedCost: money.Format(reserved), UnknownCost: money.Format(unknown),
		ConservativeCost: money.Format(conservativeCost), ConservativeProfit: money.Format(profit),
		ConservativeMarginRate: money.Format(margin),
	}}
	var err error
	if report.ByProject, err = s.financeBreakdown(ctx, "CAST(s.project_id AS CHAR)", "p.name", "JOIN projects AS p ON p.id = s.project_id"); err != nil {
		return nil, err
	}
	if report.ByService, err = s.financeBreakdown(ctx, "s.service_code", "COALESCE(svc.name, s.service_code)", "LEFT JOIN smsbower_services AS svc ON svc.code = s.service_code"); err != nil {
		return nil, err
	}
	if report.BySource, err = s.financeBreakdown(ctx, "'smsbower'", "'smsbower'", ""); err != nil {
		return nil, err
	}
	return report, nil
}

func (s *Service) financeBreakdown(ctx context.Context, keyExpr, nameExpr, join string) ([]FinanceBreakdown, error) {
	var rows []struct {
		Key        string `gorm:"column:item_key"`
		Name       string `gorm:"column:item_name"`
		OrderCount int64  `gorm:"column:order_count"`
		Revenue    string `gorm:"column:net_revenue"`
		Cost       string `gorm:"column:cost"`
	}
	query := s.dbFor(ctx).Table("smsbower_orders AS s").
		Select(keyExpr + ` AS item_key, ` + nameExpr + ` AS item_name, COUNT(*) AS order_count,
COALESCE(SUM(CASE WHEN o.debit_tx_id IS NOT NULL THEN o.pay_amount ELSE 0 END), 0) - COALESCE(SUM(o.refund_amount), 0) AS net_revenue,
COALESCE(SUM(CASE WHEN s.status IN ('completed','pending','provisioning','active','completing','cancelling','unknown') THEN s.cost_points_snapshot ELSE 0 END), 0) AS cost`).
		Joins("JOIN orders AS o ON o.order_no = s.order_no")
	if join != "" {
		query = query.Joins(join)
	}
	if err := query.Group(keyExpr + ", " + nameExpr).Order("order_count DESC, item_key ASC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load SMSBower finance breakdown: %w", err)
	}
	items := make([]FinanceBreakdown, len(rows))
	for i := range rows {
		revenue := parseDecimalOrZero(rows[i].Revenue)
		cost := parseDecimalOrZero(rows[i].Cost)
		items[i] = FinanceBreakdown{
			Key: rows[i].Key, Name: rows[i].Name, OrderCount: rows[i].OrderCount,
			NetRevenue: money.Format(revenue), Cost: money.Format(cost), Profit: money.Format(revenue.Sub(cost)),
		}
	}
	return items, nil
}

func (s *Service) ListActivations(ctx context.Context, offset, limit int) ([]ActivationItem, int64, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int64
	if err := s.dbFor(ctx).Model(&orderModel{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count SMSBower activations: %w", err)
	}
	var rows []struct {
		ID            uint       `gorm:"column:id"`
		OrderNo       string     `gorm:"column:order_no"`
		ProjectID     uint       `gorm:"column:project_id"`
		ProjectName   string     `gorm:"column:project_name"`
		ServiceCode   string     `gorm:"column:service_code"`
		Email         string     `gorm:"column:email"`
		Status        string     `gorm:"column:status"`
		ReceivedCount uint8      `gorm:"column:received_count"`
		CostPoints    string     `gorm:"column:cost_points"`
		LastSafeError string     `gorm:"column:last_safe_error"`
		StartedAt     *time.Time `gorm:"column:started_at"`
		ExpiresAt     *time.Time `gorm:"column:expires_at"`
		CompletedAt   *time.Time `gorm:"column:completed_at"`
		CreatedAt     time.Time  `gorm:"column:created_at"`
	}
	if err := s.dbFor(ctx).Table("smsbower_orders AS s").
		Select(`s.id, s.order_no, s.project_id, p.name AS project_name, s.service_code,
s.email, s.status, s.received_count, s.cost_points_snapshot AS cost_points,
s.last_safe_error, s.started_at, s.expires_at, s.completed_at, s.created_at`).
		Joins("JOIN projects AS p ON p.id = s.project_id").
		Order("s.id DESC").Offset(offset).Limit(limit).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list SMSBower activations: %w", err)
	}
	items := make([]ActivationItem, len(rows))
	for i, row := range rows {
		items[i] = ActivationItem{
			ID: row.ID, OrderNo: row.OrderNo, ProjectID: row.ProjectID, ProjectName: row.ProjectName,
			Source: "smsbower", ProviderServiceCode: row.ServiceCode, Email: row.Email,
			Status: row.Status, ReceivedCount: row.ReceivedCount, CostPoints: row.CostPoints,
			LastSafeError: row.LastSafeError, StartedAt: row.StartedAt, ExpiresAt: row.ExpiresAt,
			CompletedAt: row.CompletedAt, CreatedAt: row.CreatedAt,
		}
	}
	return items, total, nil
}

func parseDecimalOrZero(value string) decimal.Decimal {
	parsed, err := money.Parse(strings.TrimSpace(value))
	if err != nil {
		return decimal.Zero
	}
	return parsed
}
