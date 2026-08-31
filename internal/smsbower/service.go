package smsbower

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/donnel666/remail/internal/upstream"
	"github.com/hibiken/asynq"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	lifetime       = 24 * time.Hour
	pollInterval   = 5 * time.Second
	pollLease      = 30 * time.Second
	provisionLease = 2 * time.Minute
)

type TradePort interface {
	ActivateUpstreamOrder(context.Context, upstream.Activation) error
	CompleteGmailOrder(context.Context, string, string) error
	FailGmailOrder(context.Context, string, string) error
	GmailOrderReceiveUntil(context.Context, string) (time.Time, error)
}

type Service struct {
	db       *gorm.DB
	client   *client
	queue    *asynq.Client
	trade    TradePort
	notifier AlertNotifier
	logs     governanceapp.OperationLogPort
	now      func() time.Time
}

func NewService(db *gorm.DB, queue *asynq.Client) *Service {
	return &Service{
		db: db, client: newClient(), queue: queue,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) SetTrade(port TradePort)            { s.trade = port }
func (s *Service) SetNotifier(notifier AlertNotifier) { s.notifier = notifier }
func (s *Service) SetOperationLogs(logs governanceapp.OperationLogPort) {
	s.logs = logs
}

type MutationMeta struct {
	OperatorUserID uint
	RequestID      string
	Path           string
}

func (s *Service) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return s.db.WithContext(ctx)
}

func (s *Service) GetConfig(ctx context.Context) (*Config, error) {
	var model configModel
	if err := s.dbFor(ctx).First(&model, "id = 1").Error; err != nil {
		return nil, fmt.Errorf("load SMSBower config: %w", err)
	}
	return configFromModel(model), nil
}

func configFromModel(model configModel) *Config {
	return &Config{
		Enabled: model.Enabled, Configured: strings.TrimSpace(model.APIKey) != "",
		Strategy: upstream.Strategy(model.Strategy), SyncIntervalMinutes: model.SyncIntervalMinutes,
		NoCodeRefundTimeoutMinutes: uint(runtimeconfig.Int(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey, 10, 1)),
		BalanceWarningThreshold:    model.BalanceWarningThreshold,
		PointsPerUnit:              model.PointsPerUnit, MinMarginRate: model.MinMarginRate,
	}
}

func (s *Service) UpdateConfig(ctx context.Context, update ConfigUpdate, meta MutationMeta) (*Config, error) {
	apiKey := strings.TrimSpace(update.APIKey)
	threshold, thresholdErr := money.Parse(update.BalanceWarningThreshold)
	points, pointsErr := money.Parse(update.PointsPerUnit)
	margin, marginErr := money.Parse(update.MinMarginRate)
	if len(apiKey) > 512 ||
		(update.Strategy != upstream.StrategyLocalFirst && update.Strategy != upstream.StrategyUpstreamFirst) ||
		update.SyncIntervalMinutes < 1 || update.SyncIntervalMinutes > 1440 ||
		thresholdErr != nil || threshold.IsNegative() || pointsErr != nil || !points.IsPositive() ||
		marginErr != nil || margin.IsNegative() || margin.GreaterThanOrEqual(decimal.NewFromInt(1)) {
		return nil, ErrInvalidConfig
	}
	var model configModel
	err := s.mutate(ctx, meta, &governancedomain.OperationLog{
		OperationType: "smsbower.config.update", ResourceType: "smsbower_config", ResourceID: "1",
		Result: "success", SafeSummary: "updated SMSBower provider configuration",
	}, func(txCtx context.Context) error {
		db := s.dbFor(txCtx)
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model, "id = 1").Error; err != nil {
			return err
		}
		if update.Enabled && apiKey == "" && strings.TrimSpace(model.APIKey) == "" {
			return ErrInvalidConfig
		}
		updates := map[string]any{
			"enabled": update.Enabled, "strategy": string(update.Strategy),
			"sync_interval_minutes":     update.SyncIntervalMinutes,
			"balance_warning_threshold": money.Format(threshold),
			"points_per_unit":           money.Format(points), "min_margin_rate": money.Format(margin),
		}
		if apiKey != "" {
			updates["api_key"] = apiKey
		}
		if err := db.Model(&configModel{}).Where("id = 1").Updates(updates).Error; err != nil {
			return err
		}
		return db.First(&model, "id = 1").Error
	})
	if err != nil {
		return nil, err
	}
	return configFromModel(model), nil
}

func (s *Service) mutate(
	ctx context.Context,
	meta MutationMeta,
	log *governancedomain.OperationLog,
	fn func(context.Context) error,
) error {
	if s.logs == nil || meta.OperatorUserID == 0 {
		return errors.New("smsbower: operation log is required")
	}
	log.OperatorUserID = meta.OperatorUserID
	log.RequestID = strings.TrimSpace(meta.RequestID)
	log.Path = strings.TrimSpace(meta.Path)
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := platform.WithGormTx(ctx, tx)
		if err := fn(txCtx); err != nil {
			return err
		}
		if err := s.logs.Create(txCtx, log); err != nil {
			return fmt.Errorf("audit SMSBower mutation: %w", err)
		}
		return nil
	})
}

type supplyRow struct {
	ServiceCode   string     `gorm:"column:service_code"`
	Price         string     `gorm:"column:gmail_price"`
	Stock         uint       `gorm:"column:gmail_stock"`
	ServiceActive bool       `gorm:"column:service_active"`
	Balance       string     `gorm:"column:balance"`
	HealthStatus  string     `gorm:"column:health_status"`
	LastSuccessAt *time.Time `gorm:"column:last_success_at"`
	Enabled       bool       `gorm:"column:provider_enabled"`
	Configured    bool       `gorm:"column:provider_configured"`
	Strategy      string     `gorm:"column:strategy"`
	SyncInterval  uint       `gorm:"column:sync_interval_minutes"`
	PointsPerUnit string     `gorm:"column:points_per_unit"`
	MinMarginRate string     `gorm:"column:min_margin_rate"`
}

func (s *Service) Supply(ctx context.Context, demand upstream.Demand) (*upstream.SupplyQuote, error) {
	if !validDemand(demand) {
		return nil, upstream.ErrUnavailable
	}
	row, err := s.loadSupplyRow(ctx, demand)
	if err != nil {
		return nil, err
	}
	available, _, err := s.availableSupply(ctx, *row)
	if err != nil || available == 0 {
		return nil, upstream.ErrUnavailable
	}
	pay, err := money.Parse(demand.PayAmount)
	if err != nil || !pay.IsPositive() {
		return nil, upstream.ErrUnavailable
	}
	upstreamPrice, points, margin, err := pricing(row.Price, row.PointsPerUnit, row.MinMarginRate)
	if err != nil {
		return nil, upstream.ErrUnavailable
	}
	if _, _, _, safe := calculateMargin(pay, upstreamPrice, points, margin); !safe {
		return nil, upstream.ErrPriceProtected
	}
	return &upstream.SupplyQuote{Strategy: upstream.Strategy(row.Strategy), Available: uint64(available)}, nil
}

func validDemand(demand upstream.Demand) bool {
	return demand.ProjectID > 0 && demand.ProductID > 0 && demand.BuyerID > 0 &&
		demand.EmailType == upstream.EmailTypeGmail && demand.OrderType == upstream.OrderTypeCode
}

func (s *Service) loadSupplyRow(ctx context.Context, demand upstream.Demand) (*supplyRow, error) {
	var row supplyRow
	result := s.dbFor(ctx).Table("smsbower_project_routes AS r").
		Select(`r.service_code, svc.gmail_price, svc.gmail_stock, svc.active AS service_active,
account.balance, account.health_status, account.last_success_at,
cfg.enabled AS provider_enabled, (cfg.api_key <> '') AS provider_configured,
cfg.strategy, cfg.sync_interval_minutes, cfg.points_per_unit, cfg.min_margin_rate`).
		Joins("JOIN project_products AS pp ON pp.id = ? AND pp.project_id = r.project_id AND pp.type = ? AND pp.status = ? AND pp.code_enabled = ?", demand.ProductID, "gmail", "enabled", true).
		Joins("JOIN projects AS p ON p.id = r.project_id AND p.status = ?", "listed").
		Joins("JOIN smsbower_services AS svc ON svc.code = r.service_code").
		Joins("JOIN smsbower_account_state AS account ON account.id = 1").
		Joins("JOIN smsbower_config AS cfg ON cfg.id = 1").
		Where("r.project_id = ? AND r.enabled = ?", demand.ProjectID, true).
		Where("p.access_type = ? OR EXISTS (SELECT 1 FROM project_accesses AS pa WHERE pa.project_id = p.id AND pa.user_id = ?)", "public", demand.BuyerID).
		Limit(1).Scan(&row)
	if result.Error != nil {
		return nil, fmt.Errorf("load SMSBower supply: %w", result.Error)
	}
	if result.RowsAffected == 0 || !supplyRowHealthy(row, s.now()) {
		return nil, upstream.ErrUnavailable
	}
	return &row, nil
}

func supplyRowHealthy(row supplyRow, now time.Time) bool {
	if !row.Enabled || !row.Configured || !row.ServiceActive || row.Stock == 0 || row.HealthStatus != "healthy" ||
		row.LastSuccessAt == nil || row.SyncInterval == 0 {
		return false
	}
	return now.Sub(row.LastSuccessAt.UTC()) <= time.Duration(row.SyncInterval*3)*time.Minute
}

type reservations struct {
	ServiceCount uint   `gorm:"column:service_count"`
	BalanceCost  string `gorm:"column:balance_cost"`
}

func (s *Service) availableSupply(ctx context.Context, row supplyRow) (uint, decimal.Decimal, error) {
	if row.LastSuccessAt == nil {
		return 0, decimal.Zero, nil
	}
	var reserved reservations
	err := s.dbFor(ctx).Model(&orderModel{}).
		Select("COALESCE(SUM(CASE WHEN service_code = ? THEN 1 ELSE 0 END), 0) AS service_count, COALESCE(SUM(upstream_price_snapshot), 0) AS balance_cost", row.ServiceCode).
		Where("status IN ? OR (created_at >= ? AND status IN ?)",
			[]string{StatusPending, StatusProvisioning, StatusUnknown}, row.LastSuccessAt.UTC(),
			[]string{StatusActive, StatusCompleting, StatusCompleted, StatusCancelling}).
		Scan(&reserved).Error
	if err != nil {
		return 0, decimal.Zero, fmt.Errorf("load SMSBower reservations: %w", err)
	}
	price, err := money.Parse(row.Price)
	if err != nil || !price.IsPositive() {
		return 0, decimal.Zero, nil
	}
	balance, balanceErr := money.Parse(row.Balance)
	reservedCost, reserveErr := money.Parse(reserved.BalanceCost)
	if balanceErr != nil || reserveErr != nil {
		return 0, decimal.Zero, nil
	}
	availableBalance := decimal.Max(balance.Sub(reservedCost), decimal.Zero)
	stock := uint(0)
	if row.Stock > reserved.ServiceCount {
		stock = row.Stock - reserved.ServiceCount
	}
	return affordableStock(stock, availableBalance, price), availableBalance, nil
}

func (s *Service) AcceptPaidOrder(ctx context.Context, order upstream.PaidOrder) (bool, error) {
	if _, ok := platform.GormTxFromContext(ctx); !ok || !platform.HasGormRollback(ctx) {
		return false, errPaidOrderTx
	}
	order.OrderNo = strings.TrimSpace(order.OrderNo)
	var existing orderModel
	err := s.dbFor(ctx).Where("order_no = ?", order.OrderNo).Take(&existing).Error
	if err == nil {
		if existing.ProjectID != order.ProjectID || existing.ProductID != order.ProductID {
			return true, ErrInvalidRoute
		}
		if _, err := claimGmailOrderGuard(s.dbFor(ctx), order.OrderNo, s.now()); err != nil {
			return true, err
		}
		return true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if !order.Selected || order.OrderNo == "" || !validDemand(upstream.Demand{
		ProjectID: order.ProjectID, ProductID: order.ProductID, BuyerID: order.BuyerID,
		EmailType: order.EmailType, OrderType: order.OrderType, PayAmount: order.PayAmount,
	}) {
		return false, nil
	}
	db := s.dbFor(ctx)
	var config configModel
	// ponytail: singleton locks serialize the expected small upstream volume; shard reservations only if contention becomes measurable.
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&config, "id = 1").Error; err != nil {
		return true, err
	}
	var route routeModel
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&route, "project_id = ? AND enabled = ?", order.ProjectID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, upstream.ErrUnavailable
		}
		return true, err
	}
	var account accountStateModel
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "id = 1").Error; err != nil {
		return true, err
	}
	var service serviceModel
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&service, "code = ?", route.ServiceCode).Error; err != nil {
		return true, err
	}
	var productCount int64
	if err := db.Table("project_products AS pp").
		Joins("JOIN projects AS p ON p.id = pp.project_id").
		Where("pp.id = ? AND pp.project_id = ? AND pp.type = ? AND pp.status = ? AND pp.code_enabled = ? AND p.status = ?",
			order.ProductID, order.ProjectID, "gmail", "enabled", true, "listed").
		Where("p.access_type = ? OR EXISTS (SELECT 1 FROM project_accesses AS pa WHERE pa.project_id = p.id AND pa.user_id = ?)", "public", order.BuyerID).
		Count(&productCount).Error; err != nil {
		return true, err
	}
	row := supplyRow{
		ServiceCode: route.ServiceCode, Price: service.GmailPrice, Stock: service.GmailStock,
		ServiceActive: service.Active, Balance: account.Balance, HealthStatus: account.HealthStatus,
		LastSuccessAt: account.LastSuccessAt, Enabled: config.Enabled,
		Configured: strings.TrimSpace(config.APIKey) != "", Strategy: config.Strategy,
		SyncInterval: config.SyncIntervalMinutes, PointsPerUnit: config.PointsPerUnit, MinMarginRate: config.MinMarginRate,
	}
	if productCount != 1 || !supplyRowHealthy(row, s.now()) {
		return true, upstream.ErrUnavailable
	}
	if s.client == nil {
		return true, upstream.ErrUnavailable
	}
	available, _, err := s.availableSupply(ctx, row)
	if err != nil || available == 0 {
		if err != nil {
			return true, err
		}
		return true, upstream.ErrUnavailable
	}
	pay, payErr := money.Parse(order.PayAmount)
	upstreamPrice, points, minMargin, priceErr := pricing(service.GmailPrice, config.PointsPerUnit, config.MinMarginRate)
	if payErr != nil || !pay.IsPositive() || priceErr != nil {
		return true, upstream.ErrUnavailable
	}
	cost, allowedPrice, _, safe := calculateMargin(pay, upstreamPrice, points, minMargin)
	if !safe {
		return true, upstream.ErrPriceProtected
	}
	maxPrice := minDecimal(upstreamPrice, allowedPrice)
	claimed, err := claimGmailOrderGuard(db, order.OrderNo, s.now())
	if err != nil {
		return true, err
	}
	if !claimed {
		if err := db.Where("order_no = ?", order.OrderNo).Take(&existing).Error; err == nil {
			if existing.ProjectID != order.ProjectID || existing.ProductID != order.ProductID {
				return true, ErrInvalidRoute
			}
			return true, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return true, err
		}
		return true, upstream.ErrUnavailable
	}
	if s.trade == nil {
		return true, errors.New("smsbower: trade callback unavailable")
	}
	apiKey := strings.TrimSpace(config.APIKey)
	activation, err := s.client.Activate(ctx, apiKey, route.ServiceCode, maxPrice)
	if err != nil {
		return true, synchronousActivationError(err)
	}
	now := s.now()
	expiresAt := now.Add(lifetime)
	mailID := activation.MailID
	model := orderModel{
		OrderNo: order.OrderNo, ProjectID: order.ProjectID, ProductID: order.ProductID,
		ServiceCode: route.ServiceCode, RemoteMailID: &mailID, Email: activation.Email,
		Status: StatusActive, CodesJSON: "[]",
		UpstreamPriceSnapshot: money.Format(upstreamPrice), PointsPerUnitSnapshot: money.Format(points),
		CostPointsSnapshot: money.Format(cost), MaxPriceSnapshot: money.Format(maxPrice),
		NextPollAt: &now, StartedAt: &now, ExpiresAt: &expiresAt, Version: 1,
	}
	if err := db.Create(&model).Error; err != nil {
		return true, s.cancelRemoteActivation(ctx, apiKey, mailID, fmt.Errorf("create SMSBower order: %w", err))
	}
	if err := s.ensureTradeActivation(ctx, model); err != nil {
		return true, s.cancelRemoteActivation(ctx, apiKey, mailID, err)
	}
	if !platform.RegisterGormRollback(ctx, func(rollbackCtx context.Context) error {
		return s.cancelRemoteActivation(rollbackCtx, apiKey, mailID, nil)
	}) {
		return true, s.cancelRemoteActivation(ctx, apiKey, mailID, errPaidOrderTx)
	}
	return true, nil
}

func synchronousActivationError(err error) error {
	switch {
	case errors.Is(err, ErrPriceChanged):
		return upstream.ErrPriceProtected
	case errors.Is(err, ErrBadKey), errors.Is(err, ErrNoMail), errors.Is(err, ErrInsufficientBalance):
		return upstream.ErrUnavailable
	default:
		return err
	}
}

func (s *Service) cancelRemoteActivation(ctx context.Context, apiKey string, mailID uint64, cause error) error {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.client.SetStatus(cancelCtx, apiKey, mailID, 2)
	if err == nil || errors.Is(err, ErrActivationStatus) {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("cancel SMSBower activation %d: %w", mailID, err))
}

func claimGmailOrderGuard(db *gorm.DB, orderNo string, now time.Time) (bool, error) {
	guard := orderGuardModel{OrderNo: orderNo, Type: "gmail", CreatedAt: now.UTC()}
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard)
	if result.Error != nil {
		return false, fmt.Errorf("claim SMSBower order owner: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	if err := db.Where("order_no = ?", orderNo).Take(&guard).Error; err != nil {
		return false, fmt.Errorf("load SMSBower order owner: %w", err)
	}
	if guard.Type != "gmail" {
		return false, ErrInvalidRoute
	}
	return false, nil
}

func (s *Service) OwnsOrder(ctx context.Context, orderNo string) (bool, error) {
	var count int64
	err := s.dbFor(ctx).Model(&orderModel{}).Where("order_no = ?", strings.TrimSpace(orderNo)).Count(&count).Error
	return count > 0, err
}

func (s *Service) Pickup(ctx context.Context, request upstream.PickupRequest) (*upstream.PickupResult, bool, error) {
	var model orderModel
	err := s.dbFor(ctx).Where("order_no = ?", strings.TrimSpace(request.OrderNo)).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load SMSBower pickup: %w", err)
	}
	if model.Email == "" || !strings.EqualFold(strings.TrimSpace(request.Email), model.Email) {
		return nil, true, ErrPickupInvalid
	}
	codes, err := decodeCodes(model.CodesJSON)
	if err != nil {
		return nil, true, err
	}
	items := make([]upstream.Code, len(codes))
	for i := range codes {
		items[i] = upstream.Code{Seq: codes[i].Seq, Value: codes[i].Code, ReceivedAt: codes[i].ReceivedAt}
	}
	return &upstream.PickupResult{
		Email: model.Email, Codes: items,
	}, true, nil
}

func (s *Service) ListDeliveries(ctx context.Context, orderNos []string) (map[string]upstream.PickupResult, error) {
	result := make(map[string]upstream.PickupResult, len(orderNos))
	if len(orderNos) == 0 {
		return result, nil
	}
	var models []orderModel
	if err := s.dbFor(ctx).Where("order_no IN ?", orderNos).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list SMSBower deliveries: %w", err)
	}
	for _, model := range models {
		codes, err := decodeCodes(model.CodesJSON)
		if err != nil {
			return nil, err
		}
		items := make([]upstream.Code, len(codes))
		for i := range codes {
			items[i] = upstream.Code{Seq: codes[i].Seq, Value: codes[i].Code, ReceivedAt: codes[i].ReceivedAt}
		}
		result[model.OrderNo] = upstream.PickupResult{
			Email: model.Email, Codes: items,
		}
	}
	return result, nil
}

func affordableStock(stock uint, balance, unitPrice decimal.Decimal) uint {
	if stock == 0 || balance.IsNegative() || !unitPrice.IsPositive() {
		return 0
	}
	affordable := balance.Div(unitPrice).Floor()
	if !affordable.IsPositive() {
		return 0
	}
	if affordable.GreaterThanOrEqual(decimal.NewFromInt(int64(stock))) {
		return stock
	}
	return uint(affordable.IntPart())
}

func pricing(priceValue, pointsValue, marginValue string) (decimal.Decimal, decimal.Decimal, decimal.Decimal, error) {
	price, priceErr := money.Parse(priceValue)
	points, pointsErr := money.Parse(pointsValue)
	margin, marginErr := money.Parse(marginValue)
	if priceErr != nil || pointsErr != nil || marginErr != nil || !price.IsPositive() || !points.IsPositive() ||
		margin.IsNegative() || margin.GreaterThanOrEqual(decimal.NewFromInt(1)) {
		return decimal.Zero, decimal.Zero, decimal.Zero, ErrInvalidConfig
	}
	return price, points, margin, nil
}

func calculateMargin(pay, upstreamPrice, pointsPerUnit, minMargin decimal.Decimal) (cost, allowedUpstream, margin decimal.Decimal, safe bool) {
	cost = upstreamPrice.Mul(pointsPerUnit)
	if !pay.IsPositive() || !pointsPerUnit.IsPositive() {
		return cost, decimal.Zero, decimal.Zero, false
	}
	allowedUpstream = pay.Mul(decimal.NewFromInt(1).Sub(minMargin)).Div(pointsPerUnit)
	margin = pay.Sub(cost).Div(pay)
	return cost, allowedUpstream, margin, upstreamPrice.LessThanOrEqual(allowedUpstream)
}

func minDecimal(left, right decimal.Decimal) decimal.Decimal {
	if left.LessThan(right) {
		return left
	}
	return right
}
