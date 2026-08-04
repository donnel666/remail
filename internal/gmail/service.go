package gmail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	gmailLifetime       = 24 * time.Hour
	gmailPollInterval   = 5 * time.Second
	gmailProvisionLease = 2 * time.Minute
)

type TradePort interface {
	ActivateGmailOrder(ctx context.Context, req tradeapp.ActivateGmailOrderRequest) error
	CompleteGmailOrder(ctx context.Context, orderNo, reason string) error
	FailGmailOrder(ctx context.Context, orderNo, reason string) error
}

type Alert struct {
	ID      string
	Subject string
	Body    string
}

type AlertNotifier interface {
	NotifySMSBower(ctx context.Context, alert Alert) error
}

type Service struct {
	db                  *gorm.DB
	client              *SMSBowerClient
	queue               *asynq.Client
	trade               TradePort
	notifier            AlertNotifier
	mail                MailIngestPort
	redis               redis.UniversalClient
	files               governanceapp.FilePort
	logs                *governanceinfra.OperationLogRepo
	systemLogs          *governanceinfra.SystemLogRepo
	backgroundExecution *platform.BackgroundLoadController
	validateImportOwner func(context.Context, uint) (bool, error)
	now                 func() time.Time
	pick                func(uint64) uint64
	fetch               localGmailFetchFunc
	pickup              *localGmailPickupClient
}

func NewService(db *gorm.DB, queue *asynq.Client) *Service {
	pickup := newLocalGmailPickupClient(nil)
	service := &Service{
		db: db, client: NewSMSBowerClient(), queue: queue,
		now: func() time.Time { return time.Now().UTC() }, pick: rand.Uint64N,
		fetch: pickup.Fetch, pickup: pickup,
	}
	if db != nil {
		service.logs = governanceinfra.NewOperationLogRepo(db)
		service.systemLogs = governanceinfra.NewSystemLogRepo(db)
	}
	return service
}

func (s *Service) SetTrade(port TradePort)            { s.trade = port }
func (s *Service) SetNotifier(notifier AlertNotifier) { s.notifier = notifier }
func (s *Service) SetMailIngest(port MailIngestPort)  { s.mail = port }
func (s *Service) SetResourceImportDependencies(redisClient redis.UniversalClient, files governanceapp.FilePort) {
	if s != nil {
		s.redis, s.files = redisClient, files
	}
}
func (s *Service) SetImportOwnerValidator(validate func(context.Context, uint) (bool, error)) {
	if s != nil {
		s.validateImportOwner = validate
	}
}

func (s *Service) SetBackgroundExecutionGate(gate *platform.BackgroundLoadController) {
	if s != nil {
		s.backgroundExecution = gate
	}
}

func (s *Service) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return s.db.WithContext(ctx)
}

func (s *Service) CheckSupply(
	ctx context.Context,
	projectID, productID, buyerUserID uint,
	mode tradedomain.ServiceMode,
	policy tradedomain.SupplyPolicy,
	payAmount string,
) (*tradeapp.GmailSupplyQuote, error) {
	if projectID == 0 || productID == 0 || buyerUserID == 0 ||
		(mode != tradedomain.ServiceModeCode && mode != tradedomain.ServiceModePurchase) ||
		(policy != tradedomain.SupplyPolicyPrivateFirst && policy != tradedomain.SupplyPolicyPublicOnly) {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	pay, err := money.Parse(payAmount)
	if err != nil || pay.IsNegative() {
		return nil, tradedomain.ErrInvalidOrderRequest
	}

	candidates := make([]*tradeapp.GmailSupplyQuote, 0, 2)
	priceProtected := false
	local, err := s.checkLocalSupply(ctx, projectID, productID, buyerUserID, mode, policy)
	if err == nil {
		candidates = append(candidates, local)
	} else if errors.Is(err, tradedomain.ErrUpstreamPriceProtected) {
		priceProtected = true
	} else if !errors.Is(err, tradedomain.ErrUpstreamUnavailable) {
		return nil, err
	}
	if pay.IsPositive() {
		upstream, err := s.checkSMSBowerSupply(ctx, projectID, mode, payAmount)
		if err == nil {
			candidates = append(candidates, upstream)
		} else if errors.Is(err, tradedomain.ErrUpstreamPriceProtected) {
			priceProtected = true
		} else if !errors.Is(err, tradedomain.ErrUpstreamUnavailable) {
			return nil, err
		}
	}
	if len(candidates) == 0 {
		if priceProtected {
			return nil, tradedomain.ErrUpstreamPriceProtected
		}
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	return s.pickSupplyCandidate(candidates), nil
}

func (s *Service) checkLocalSupply(
	ctx context.Context,
	projectID, productID, buyerUserID uint,
	mode tradedomain.ServiceMode,
	policy tradedomain.SupplyPolicy,
) (*tradeapp.GmailSupplyQuote, error) {
	modeColumn, costColumn := "pp.code_enabled", "code_supplier_price"
	if mode == tradedomain.ServiceModePurchase {
		modeColumn, costColumn = "pp.purchase_enabled", "purchase_supplier_price"
	}
	var row struct {
		Cost       string `gorm:"column:cost_points"`
		MainWeight int    `gorm:"column:main_weight"`
		DotWeight  int    `gorm:"column:dot_weight"`
		PlusWeight int    `gorm:"column:plus_weight"`
		Available  uint64 `gorm:"column:available"`
	}
	result := s.dbFor(ctx).Table("project_products AS pp").
		Select(`pp.`+costColumn+` AS cost_points,
	pp.main_weight, pp.dot_weight, pp.plus_weight,
	(SELECT COUNT(*) FROM gmail_resources AS gr
	 JOIN email_resources AS er ON er.id = gr.id AND er.type = 'gmail'
	 JOIN users AS owner ON owner.id = er.owner_user_id
	 WHERE gr.status IN (?, ?)
	   AND (
	     (pp.main_weight > 0
	       AND NOT EXISTS (SELECT 1 FROM gmail_allocations AS active WHERE active.resource_id = gr.id AND active.mailbox = 'main' AND active.status = ?)
	       AND NOT EXISTS (SELECT 1 FROM gmail_allocations AS history WHERE history.resource_id = gr.id AND history.project_id = pp.project_id AND history.mailbox = 'main'))
	     OR (pp.dot_weight > 0 AND gr.email LIKE '__%@%')
	     OR pp.plus_weight > 0
	   )
	   AND ((? = 'private_first' AND gr.for_sale = FALSE AND er.owner_user_id = ?)
	        OR (gr.for_sale = TRUE AND owner.status = 'active' AND owner.role IN ('supplier','admin','super_admin')))) AS available`,
			LocalResourceNormal, localResourceRollbackNormal, AllocationStatusAllocated, string(policy), buyerUserID).
		Joins("JOIN projects AS p ON p.id = pp.project_id").
		Where("pp.id = ? AND pp.project_id = ? AND pp.type = ? AND pp.status = ? AND "+modeColumn+" = ?", productID, projectID, "gmail", "enabled", true).
		Where("p.status = ?", "listed").
		Where("p.access_type = ? OR EXISTS (SELECT 1 FROM project_accesses AS pa WHERE pa.project_id = p.id AND pa.user_id = ?)", "public", buyerUserID).
		Limit(1).Scan(&row)
	if result.Error != nil {
		return nil, fmt.Errorf("load local Gmail supply: %w", result.Error)
	}
	if result.RowsAffected == 0 || row.Available == 0 || row.MainWeight+row.DotWeight+row.PlusWeight <= 0 {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	cost, err := money.Parse(row.Cost)
	if err != nil || cost.IsNegative() {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	return &tradeapp.GmailSupplyQuote{
		Source: SourceLocal, UpstreamPrice: "0.00", PointsPerUnit: "1.00",
		CostPoints: money.Format(cost), MaxPrice: "0.00", Available: row.Available,
	}, nil
}

func (s *Service) pickSupplyCandidate(candidates []*tradeapp.GmailSupplyQuote) *tradeapp.GmailSupplyQuote {
	var total uint64
	for _, candidate := range candidates {
		if candidate != nil {
			total += candidate.Available
		}
	}
	if total == 0 {
		return candidates[0]
	}
	ticket := uint64(0)
	if s.pick != nil {
		ticket = s.pick(total)
	}
	for _, candidate := range candidates {
		if candidate == nil || candidate.Available == 0 {
			continue
		}
		if ticket < candidate.Available {
			return candidate
		}
		ticket -= candidate.Available
	}
	return candidates[len(candidates)-1]
}

func (s *Service) checkSMSBowerSupply(ctx context.Context, projectID uint, mode tradedomain.ServiceMode, payAmount string) (*tradeapp.GmailSupplyQuote, error) {
	// SMSBower Mails returns a temporary address and codes, not the password/2FA/app-password required by purchase delivery.
	if projectID == 0 || mode != tradedomain.ServiceModeCode {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	pay, err := money.Parse(payAmount)
	if err != nil || !pay.IsPositive() {
		return nil, tradedomain.ErrInvalidOrderRequest
	}
	if !runtimeconfig.Bool("smsbower_enabled", false) || !smsbowerModeEnabled(mode) || strings.TrimSpace(runtimeconfig.String("smsbower_api_key", "")) == "" {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	type supplyRow struct {
		ProviderServiceCode string     `gorm:"column:provider_service_code"`
		Price               string     `gorm:"column:gmail_price"`
		Stock               uint       `gorm:"column:gmail_stock"`
		ServiceActive       bool       `gorm:"column:service_active"`
		Balance             string     `gorm:"column:balance"`
		HealthStatus        string     `gorm:"column:health_status"`
		LastSuccessAt       *time.Time `gorm:"column:last_success_at"`
	}
	var row supplyRow
	result := s.dbFor(ctx).
		Table("gmail_supply_routes AS r").
		Select(`r.provider_service_code, COALESCE(svc.gmail_price, 0) AS gmail_price,
COALESCE(svc.gmail_stock, 0) AS gmail_stock, COALESCE(svc.active, 0) AS service_active,
COALESCE(account.balance, 0) AS balance, COALESCE(account.health_status, '') AS health_status,
account.last_success_at`).
		Joins("JOIN project_products AS pp ON pp.project_id = r.project_id AND pp.type = ? AND pp.status = ?", "gmail", "enabled").
		Joins("LEFT JOIN smsbower_services AS svc ON svc.code = r.provider_service_code").
		Joins("LEFT JOIN smsbower_account_state AS account ON account.id = 1").
		Where("r.project_id = ? AND r.source = ? AND pp.code_enabled = ?", projectID, SourceSMSBower, true).
		Limit(1).Scan(&row)
	if result.Error != nil {
		return nil, fmt.Errorf("load SMSBower mapping: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	staleAfter := time.Duration(runtimeconfig.Int("smsbower_sync_interval_minutes", 5, 1)*3) * time.Minute
	if strings.TrimSpace(row.ProviderServiceCode) == "" || !row.ServiceActive || row.Stock == 0 || row.HealthStatus != "healthy" ||
		row.LastSuccessAt == nil || s.now().Sub(row.LastSuccessAt.UTC()) > staleAfter {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	upstream, err := money.Parse(row.Price)
	if err != nil || !upstream.IsPositive() {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	balance, err := money.Parse(row.Balance)
	available := affordableStock(row.Stock, balance, upstream)
	if err != nil || available == 0 {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	pointsPerUnit, minMargin, err := upstreamPricingSettings()
	if err != nil {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	cost, allowedPrice, _, safe := calculateSupplyMargin(pay, upstream, pointsPerUnit, minMargin)
	if !safe {
		return nil, tradedomain.ErrUpstreamPriceProtected
	}
	return &tradeapp.GmailSupplyQuote{
		Source: SourceSMSBower, ProviderServiceCode: row.ProviderServiceCode,
		UpstreamPrice: money.Format(upstream), PointsPerUnit: money.Format(pointsPerUnit),
		CostPoints: money.Format(cost), MaxPrice: money.Format(minDecimal(upstream, allowedPrice)), Available: uint64(available),
	}, nil
}

func smsbowerModeEnabled(mode tradedomain.ServiceMode) bool {
	return mode == tradedomain.ServiceModeCode && runtimeconfig.Bool("smsbower_code_enabled", true)
}

func (s *Service) ListInventory(ctx context.Context, projectIDs []uint) ([]InventoryItem, error) {
	projectIDs = uniqueUintValues(projectIDs)
	if len(projectIDs) == 0 {
		return []InventoryItem{}, nil
	}
	type inventoryRow struct {
		ProjectID           uint       `gorm:"column:project_id"`
		ProductID           uint       `gorm:"column:product_id"`
		ProductStatus       string     `gorm:"column:product_status"`
		CodeEnabled         bool       `gorm:"column:code_enabled"`
		PurchaseEnabled     bool       `gorm:"column:purchase_enabled"`
		CodePrice           string     `gorm:"column:code_price"`
		PurchasePrice       string     `gorm:"column:purchase_price"`
		ProviderServiceCode string     `gorm:"column:provider_service_code"`
		UpstreamPrice       string     `gorm:"column:gmail_price"`
		Stock               uint       `gorm:"column:gmail_stock"`
		ServiceActive       bool       `gorm:"column:service_active"`
		Balance             string     `gorm:"column:balance"`
		HealthStatus        string     `gorm:"column:health_status"`
		LastSuccessAt       *time.Time `gorm:"column:last_success_at"`
		LocalStock          int64      `gorm:"column:local_stock"`
	}
	var rows []inventoryRow
	if err := s.dbFor(ctx).Table("project_products AS pp").
		Select(`pp.project_id, pp.id AS product_id, pp.status AS product_status, pp.code_enabled, pp.purchase_enabled,
	pp.code_price, pp.purchase_price,
	COALESCE(r.provider_service_code, '') AS provider_service_code,
	COALESCE(svc.gmail_price, 0) AS gmail_price,
	COALESCE(svc.gmail_stock, 0) AS gmail_stock, COALESCE(svc.active, 0) AS service_active,
	COALESCE(account.balance, 0) AS balance, COALESCE(account.health_status, '') AS health_status,
	account.last_success_at,
	(SELECT COUNT(*) FROM gmail_resources AS gr
	 WHERE gr.status IN (?, ?)
	   AND NOT EXISTS (SELECT 1 FROM gmail_allocations AS active WHERE active.resource_id = gr.id AND active.status = ?)
	   AND NOT EXISTS (SELECT 1 FROM gmail_allocations AS history WHERE history.resource_id = gr.id AND history.project_id = pp.project_id)) AS local_stock`, LocalResourceNormal, localResourceRollbackNormal, AllocationStatusAllocated).
		Joins("LEFT JOIN gmail_supply_routes AS r ON r.project_id = pp.project_id AND r.source = ?", SourceSMSBower).
		Joins("LEFT JOIN smsbower_services AS svc ON svc.code = r.provider_service_code").
		Joins("LEFT JOIN smsbower_account_state AS account ON account.id = 1").
		Where("pp.project_id IN ? AND pp.type = ?", projectIDs, "gmail").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load Gmail inventory: %w", err)
	}
	upstreamConfigured := runtimeconfig.Bool("smsbower_enabled", false) && strings.TrimSpace(runtimeconfig.String("smsbower_api_key", "")) != ""
	pointsPerUnit, upstreamMinMargin, upstreamSettingsErr := upstreamPricingSettings()
	minimumRatio := decimal.NewFromInt(1)
	var minimumRatioValue string
	if err := s.dbFor(ctx).Table("user_groups").Select("COALESCE(MIN(price_discount_ratio), 1)").Where("enabled = ?", true).Scan(&minimumRatioValue).Error; err != nil {
		return nil, fmt.Errorf("load minimum user price ratio: %w", err)
	}
	if parsed, err := money.Parse(minimumRatioValue); err == nil && !parsed.IsNegative() {
		minimumRatio = parsed
	}
	staleAfter := time.Duration(runtimeconfig.Int("smsbower_sync_interval_minutes", 5, 1)*3) * time.Minute
	items := make([]InventoryItem, 0, len(rows))
	for _, row := range rows {
		item := InventoryItem{ProjectID: row.ProjectID, ProductID: row.ProductID}
		if row.ProductStatus != "enabled" {
			items = append(items, item)
			continue
		}
		if row.LocalStock > 0 && row.CodeEnabled {
			item.CodeAvailable += row.LocalStock
		}
		if row.LocalStock > 0 && row.PurchaseEnabled {
			item.PurchaseAvailable += row.LocalStock
		}
		if !upstreamConfigured || upstreamSettingsErr != nil || row.ProviderServiceCode == "" || !row.ServiceActive || row.Stock == 0 ||
			row.HealthStatus != "healthy" || row.LastSuccessAt == nil || s.now().Sub(row.LastSuccessAt.UTC()) > staleAfter {
			items = append(items, item)
			continue
		}
		upstream, upstreamErr := money.Parse(row.UpstreamPrice)
		balance, balanceErr := money.Parse(row.Balance)
		if upstreamErr != nil || balanceErr != nil || !upstream.IsPositive() {
			items = append(items, item)
			continue
		}
		// ponytail: this is a non-reserving hint; checkout repeats price, balance, stock and margin checks.
		upstreamStock := int64(affordableStock(row.Stock, balance, upstream))
		upstreamSafe := func(enabled bool, saleValue string) bool {
			sale, saleErr := money.Parse(saleValue)
			if !enabled || saleErr != nil {
				return false
			}
			_, _, _, safe := calculateSupplyMargin(sale.Mul(minimumRatio), upstream, pointsPerUnit, upstreamMinMargin)
			return safe
		}
		if smsbowerModeEnabled(tradedomain.ServiceModeCode) && upstreamSafe(row.CodeEnabled, row.CodePrice) {
			item.CodeAvailable += upstreamStock
		}
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

func upstreamPricingSettings() (decimal.Decimal, decimal.Decimal, error) {
	pointsPerUnit, err := money.Parse(runtimeconfig.String("smsbower_points_per_unit", "1"))
	if err != nil || !pointsPerUnit.IsPositive() {
		return decimal.Zero, decimal.Zero, ErrInvalidRoute
	}
	minMargin, err := minimumMarginSetting()
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	return pointsPerUnit, minMargin, nil
}

func minimumMarginSetting() (decimal.Decimal, error) {
	minMargin, err := money.Parse(runtimeconfig.String("smsbower_min_margin_rate", "0.10"))
	if err != nil || minMargin.IsNegative() || minMargin.GreaterThanOrEqual(decimal.NewFromInt(1)) {
		return decimal.Zero, ErrInvalidRoute
	}
	return minMargin, nil
}

func calculateSupplyMargin(pay, upstream, pointsPerUnit, minMargin decimal.Decimal) (cost, allowedUpstream, margin decimal.Decimal, safe bool) {
	cost = upstream.Mul(pointsPerUnit)
	if !pay.IsPositive() || !pointsPerUnit.IsPositive() {
		return cost, decimal.Zero, decimal.Zero, false
	}
	allowedUpstream = pay.Mul(decimal.NewFromInt(1).Sub(minMargin)).Div(pointsPerUnit)
	margin = pay.Sub(cost).Div(pay)
	return cost, allowedUpstream, margin, upstream.LessThanOrEqual(allowedUpstream)
}

func minDecimal(left, right decimal.Decimal) decimal.Decimal {
	if left.LessThan(right) {
		return left
	}
	return right
}

func (s *Service) FindSessionID(ctx context.Context, orderNo string) (uint, error) {
	var id uint
	err := s.dbFor(ctx).Model(&sessionModel{}).Where("order_no = ?", strings.TrimSpace(orderNo)).Pluck("id", &id).Error
	if err != nil {
		return 0, fmt.Errorf("find Gmail session: %w", err)
	}
	return id, nil
}

func (s *Service) CancelGmailOrder(ctx context.Context, orderNo string) error {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return ErrSessionMissing
	}
	now := s.now()
	var session sessionModel
	schedule := false
	finishLocal := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).Take(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if session.Source == SourceLocal {
			switch session.Status {
			case SessionPending, SessionProvisioning, SessionActive, SessionCancelling:
				if err := tx.Model(&sessionModel{}).Where("id = ?", session.ID).Updates(map[string]any{
					"status": SessionCancelled, "pending_remote_action": "", "completed_at": now,
					"next_poll_at": now, "last_safe_error": "Gmail 接码会话已取消，订单已退款。",
					"version": gorm.Expr("version + 1"),
				}).Error; err != nil {
					return err
				}
				session.Status = SessionCancelled
				session.CompletedAt = &now
				session.LastSafeError = "Gmail 接码会话已取消，订单已退款。"
				finishLocal = true
			case SessionCancelled, SessionFailed:
				finishLocal = true
			}
			return nil
		}
		switch session.Status {
		case SessionActive, SessionCompleting:
			if err := tx.Model(&sessionModel{}).Where("id = ?", session.ID).Updates(map[string]any{
				"status": SessionCancelling, "pending_remote_action": ActionCancel,
				"next_poll_at": now, "last_safe_error": "", "version": gorm.Expr("version + 1"),
			}).Error; err != nil {
				return err
			}
			schedule = true
		case SessionCancelling:
			if err := tx.Model(&sessionModel{}).Where("id = ?", session.ID).Update("next_poll_at", now).Error; err != nil {
				return err
			}
			schedule = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cancel Gmail session: %w", err)
	}
	if finishLocal {
		if err := s.finishLocalSession(ctx, session); err != nil {
			return fmt.Errorf("cancel local Gmail session: %w", err)
		}
		return nil
	}
	if schedule {
		return s.schedulePoll(context.WithoutCancel(ctx), session.ID)
	}
	return nil
}

func (s *Service) CreateSession(ctx context.Context, cmd tradeapp.GmailSessionCommand) (uint, error) {
	cmd.OrderNo = strings.TrimSpace(cmd.OrderNo)
	quote := cmd.Quote
	quote.Source = strings.TrimSpace(quote.Source)
	quote.ProviderServiceCode = strings.TrimSpace(quote.ProviderServiceCode)
	if cmd.OrderNo == "" || quote.Source != SourceSMSBower && quote.Source != SourceLocal ||
		quote.Source == SourceSMSBower && quote.ProviderServiceCode == "" ||
		quote.Source == SourceLocal && (cmd.ProjectID == 0 || cmd.ProductID == 0) {
		return 0, ErrInvalidRoute
	}
	for _, value := range []string{quote.UpstreamPrice, quote.PointsPerUnit, quote.CostPoints, quote.MaxPrice} {
		parsed, err := money.Parse(value)
		if err != nil || parsed.IsNegative() {
			return 0, ErrInvalidRoute
		}
	}
	var model sessionModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("order_no = ?", cmd.OrderNo).Take(&model).Error
		if err == nil {
			if model.Source != quote.Source || model.ServiceMode != string(tradedomain.ServiceModeCode) ||
				quote.Source == SourceSMSBower && model.ProviderServiceCode != quote.ProviderServiceCode {
				return ErrInvalidRoute
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		model = sessionModel{
			OrderNo: cmd.OrderNo, Source: quote.Source, ProviderServiceCode: quote.ProviderServiceCode,
			ServiceMode: string(tradedomain.ServiceModeCode), Status: SessionPending, CodesJSON: []byte("[]"),
			UpstreamPriceSnapshot: quote.UpstreamPrice, PointsPerUnitSnapshot: quote.PointsPerUnit,
			CostPointsSnapshot: quote.CostPoints, MaxPriceSnapshot: quote.MaxPrice, Version: 1,
		}
		if quote.Source == SourceLocal {
			var allocation allocationModel
			if err := tx.Where("order_no = ?", cmd.OrderNo).Take(&allocation).Error; err != nil {
				return fmt.Errorf("load local Gmail code allocation: %w", err)
			}
			if allocation.GuardType != "gmail" || allocation.Source != SourceLocal ||
				allocation.ServiceMode != string(tradedomain.ServiceModeCode) || allocation.ProjectID != cmd.ProjectID ||
				allocation.ProductID != cmd.ProductID || allocation.ResourceID == nil || allocation.Status != AllocationStatusAllocated ||
				!isGmailMailbox(allocation.Mailbox) ||
				(allocation.SupplyScope != AllocationSupplyOwned && allocation.SupplyScope != AllocationSupplyPublic) {
				return ErrInvalidRoute
			}
			var resource localResourceModel
			if err := tx.Where("id = ?", *allocation.ResourceID).Take(&resource).Error; err != nil {
				return fmt.Errorf("load local Gmail code resource: %w", err)
			}
			now := s.now()
			expiresAt := now.Add(gmailLifetime)
			model.SourceRef = strconv.FormatUint(uint64(allocation.ID), 10)
			model.Email = allocation.Email
			model.Status = SessionActive
			model.StartedAt = &now
			model.ExpiresAt = &expiresAt
			model.NextPollAt = &now
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model)
		if result.Error != nil {
			return fmt.Errorf("create Gmail session: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return tx.Where("order_no = ?", cmd.OrderNo).Take(&model).Error
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return model.ID, nil
}

func (s *Service) ListGmailDeliveries(ctx context.Context, orderNos []string) (map[string]tradeapp.GmailDeliverySummary, error) {
	result := make(map[string]tradeapp.GmailDeliverySummary, len(orderNos))
	if len(orderNos) == 0 {
		return result, nil
	}
	var allocations []allocationModel
	if err := s.dbFor(ctx).Where("order_no IN ?", orderNos).Find(&allocations).Error; err != nil {
		return nil, fmt.Errorf("list Gmail allocations: %w", err)
	}
	for _, allocation := range allocations {
		result[allocation.OrderNo] = tradeapp.GmailDeliverySummary{AllocationID: allocation.ID}
	}
	var sessions []sessionModel
	if err := s.dbFor(ctx).Where("order_no IN ?", orderNos).Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("list Gmail deliveries: %w", err)
	}
	for _, session := range sessions {
		codes, err := decodeCodes(session.CodesJSON)
		if err != nil {
			return nil, err
		}
		items := make([]tradeapp.GmailCode, len(codes))
		for i := range codes {
			items[i] = tradeapp.GmailCode{Seq: codes[i].Seq, Code: codes[i].Code, ReceivedAt: codes[i].ReceivedAt}
		}
		delivery := result[session.OrderNo]
		delivery.Codes = items
		delivery.ReceivedCount = int(session.ReceivedCount)
		delivery.MaxCodes = MaxCodes
		delivery.ExpiresAt = session.ExpiresAt
		result[session.OrderNo] = delivery
	}
	return result, nil
}

func (s *Service) PickupByOrder(ctx context.Context, orderNo, email string) (*CodeOnlyPickup, bool, error) {
	var session sessionModel
	err := s.dbFor(ctx).Where("order_no = ?", strings.TrimSpace(orderNo)).Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load Gmail pickup: %w", err)
	}
	if session.Email == "" || !strings.EqualFold(strings.TrimSpace(email), session.Email) {
		return nil, true, ErrPickupInvalid
	}
	codes, err := decodeCodes(session.CodesJSON)
	if err != nil {
		return nil, true, err
	}
	return &CodeOnlyPickup{
		Email: session.Email, Codes: codes, ReceivedCount: int(session.ReceivedCount), MaxCodes: MaxCodes, ExpiresAt: session.ExpiresAt,
	}, true, nil
}

func (s *Service) AccountStatus(ctx context.Context) (*AccountStatus, error) {
	var state accountStateModel
	if err := s.dbFor(ctx).First(&state, "id = 1").Error; err != nil {
		return nil, fmt.Errorf("load SMSBower account status: %w", err)
	}
	return &AccountStatus{
		Enabled: runtimeconfig.Bool("smsbower_enabled", false), Configured: strings.TrimSpace(runtimeconfig.String("smsbower_api_key", "")) != "",
		Balance: state.Balance, HealthStatus: state.HealthStatus, ConsecutiveFailures: state.ConsecutiveFailures,
		LastSafeError: state.LastSafeError, LastSyncedAt: state.LastSyncedAt, LastSuccessAt: state.LastSuccessAt,
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
			Code: models[i].Code, Name: models[i].Name, GmailPrice: models[i].GmailPrice, GmailStock: models[i].GmailStock,
			PreviousPrice: models[i].PreviousPrice, Active: models[i].Active, PriceChangedAt: models[i].PriceChangedAt, LastSeenAt: models[i].LastSeenAt,
		}
	}
	return items, nil
}

func (s *Service) PutMapping(ctx context.Context, projectID uint, serviceCode string) error {
	serviceCode = strings.TrimSpace(serviceCode)
	if projectID == 0 || serviceCode == "" || len(serviceCode) > 64 {
		return ErrInvalidRoute
	}
	model := routeModel{
		ProjectID: projectID, Source: SourceSMSBower, ProviderServiceCode: serviceCode,
	}
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedProjectID uint
		if err := tx.Table("projects").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ?", projectID).Take(&lockedProjectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidRoute
			}
			return fmt.Errorf("lock SMSBower mapping project: %w", err)
		}
		var serviceCount int64
		if err := tx.Model(&serviceModel{}).Where("code = ?", serviceCode).Count(&serviceCount).Error; err != nil {
			return fmt.Errorf("check SMSBower service: %w", err)
		}
		if serviceCount == 0 {
			return ErrInvalidRoute
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "project_id"}, {Name: "source"}},
			DoUpdates: clause.Assignments(map[string]any{
				"provider_service_code": serviceCode, "updated_at": s.now(),
			}),
		}).Create(&model).Error; err != nil {
			return err
		}
		return nil
	})
}

func (s *Service) DeleteMapping(ctx context.Context, projectID uint) error {
	if projectID == 0 {
		return ErrInvalidRoute
	}
	return s.dbFor(ctx).Where("project_id = ? AND source = ?", projectID, SourceSMSBower).Delete(&routeModel{}).Error
}

func (s *Service) ListMappings(ctx context.Context) ([]MappingItem, error) {
	var rows []struct {
		ProjectID           uint   `gorm:"column:project_id"`
		ProjectName         string `gorm:"column:project_name"`
		CodePrice           string `gorm:"column:code_price"`
		PurchasePrice       string `gorm:"column:purchase_price"`
		ProviderServiceCode string `gorm:"column:provider_service_code"`
		ServiceName         string `gorm:"column:service_name"`
		UpstreamPrice       string `gorm:"column:gmail_price"`
	}
	if err := s.dbFor(ctx).Table("gmail_supply_routes AS r").
		Select(`p.id AS project_id, p.name AS project_name,
COALESCE(pp.code_price, 0) AS code_price, COALESCE(pp.purchase_price, 0) AS purchase_price,
COALESCE(r.provider_service_code, '') AS provider_service_code,
COALESCE(svc.name, '') AS service_name,
	COALESCE(svc.gmail_price, 0) AS gmail_price`).
		Joins("JOIN projects AS p ON p.id = r.project_id").
		Joins("LEFT JOIN project_products AS pp ON pp.project_id = p.id AND pp.type = ?", "gmail").
		Joins("LEFT JOIN smsbower_services AS svc ON svc.code = r.provider_service_code").
		Where("r.source = ?", SourceSMSBower).
		Order("p.name ASC, p.id ASC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Gmail supply mappings: %w", err)
	}
	pointsPerUnit, err := money.Parse(runtimeconfig.String("smsbower_points_per_unit", "1"))
	if err != nil || !pointsPerUnit.IsPositive() {
		pointsPerUnit = decimal.Zero
	}
	items := make([]MappingItem, len(rows))
	for i, row := range rows {
		upstream := parseDecimalOrZero(row.UpstreamPrice)
		cost := upstream.Mul(pointsPerUnit)
		items[i] = MappingItem{
			ProjectID: row.ProjectID, ProjectName: row.ProjectName,
			CodePrice: row.CodePrice, PurchasePrice: row.PurchasePrice,
			ProviderServiceCode: row.ProviderServiceCode, ProviderServiceName: row.ServiceName,
			UpstreamPrice: row.UpstreamPrice, CostPoints: money.Format(cost),
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
	if err := s.dbFor(ctx).Table("orders AS o").
		Select(`COUNT(*) AS order_count,
COALESCE(SUM(CASE WHEN ga.id IS NOT NULL THEN 1 ELSE 0 END), 0) AS activation_count,
COALESCE(SUM(CASE WHEN s.id IS NOT NULL AND s.received_count = 0 THEN 1 ELSE 0 END), 0) AS zero_code_count,
COALESCE(SUM(CASE WHEN s.received_count = 1 THEN 1 ELSE 0 END), 0) AS one_code_count,
COALESCE(SUM(CASE WHEN s.received_count = 2 THEN 1 ELSE 0 END), 0) AS two_code_count,
COALESCE(SUM(CASE WHEN s.received_count = 3 THEN 1 ELSE 0 END), 0) AS three_code_count,
COALESCE(SUM(CASE WHEN o.debit_tx_id IS NOT NULL THEN o.pay_amount ELSE 0 END), 0) AS sales,
COALESCE(SUM(o.refund_amount), 0) AS refunds,
COALESCE(SUM(CASE WHEN s.status = 'completed' THEN s.cost_points_snapshot ELSE 0 END), 0)
	+ COALESCE(SUM(CASE WHEN s.id IS NULL AND ga.source = 'local' AND ga.service_mode = 'purchase' THEN ga.cost_points_snapshot ELSE 0 END), 0) AS settled_cost,
COALESCE(SUM(CASE WHEN s.status IN ('pending','provisioning','active','completing','cancelling') THEN s.cost_points_snapshot ELSE 0 END), 0) AS reserved_cost,
COALESCE(SUM(CASE WHEN s.status = 'unknown' THEN s.cost_points_snapshot ELSE 0 END), 0) AS unknown_cost`).
		Joins("LEFT JOIN gmail_code_sessions AS s ON s.order_no = o.order_no").
		Joins("LEFT JOIN gmail_allocations AS ga ON ga.order_no = o.order_no").
		Where("o.product_type = ?", "gmail").Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("load Gmail finance overview: %w", err)
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
		ZeroCodeCount: row.ZeroCodeCount, OneCodeCount: row.OneCodeCount, TwoCodeCount: row.TwoCodeCount, ThreeCodeCount: row.ThreeCodeCount,
		Sales: money.Format(sales), Refunds: money.Format(refunds), NetRevenue: money.Format(netRevenue),
		SettledCost: money.Format(settled), ReservedCost: money.Format(reserved), UnknownCost: money.Format(unknown),
		ConservativeCost: money.Format(conservativeCost), ConservativeProfit: money.Format(profit), ConservativeMarginRate: money.Format(margin),
	}}
	var err error
	if report.ByProject, err = s.financeBreakdown(ctx, "CAST(o.project_id AS CHAR)", "p.name", "JOIN projects AS p ON p.id = o.project_id"); err != nil {
		return nil, err
	}
	if report.ByService, err = s.financeBreakdown(ctx,
		"CASE WHEN ga.resource_id IS NOT NULL THEN ga.source ELSE s.provider_service_code END",
		"CASE WHEN ga.resource_id IS NOT NULL THEN '自有 Gmail' ELSE COALESCE(svc.name, s.provider_service_code) END",
		"LEFT JOIN smsbower_services AS svc ON svc.code = s.provider_service_code"); err != nil {
		return nil, err
	}
	if report.BySource, err = s.financeBreakdown(ctx,
		"COALESCE(ga.source, s.source, '')",
		"COALESCE(ga.source, s.source, '')", ""); err != nil {
		return nil, err
	}
	return report, nil
}

func (s *Service) financeBreakdown(ctx context.Context, keyExpr, nameExpr, join string) ([]FinanceBreakdown, error) {
	type row struct {
		Key        string `gorm:"column:item_key"`
		Name       string `gorm:"column:item_name"`
		OrderCount int64  `gorm:"column:order_count"`
		Revenue    string `gorm:"column:net_revenue"`
		Cost       string `gorm:"column:cost"`
	}
	var rows []row
	query := s.dbFor(ctx).Table("orders AS o").
		Select(keyExpr+` AS item_key, `+nameExpr+` AS item_name, COUNT(*) AS order_count,
COALESCE(SUM(CASE WHEN o.debit_tx_id IS NOT NULL THEN o.pay_amount ELSE 0 END), 0) - COALESCE(SUM(o.refund_amount), 0) AS net_revenue,
COALESCE(SUM(CASE WHEN s.status IN ('completed','pending','provisioning','active','completing','cancelling','unknown') THEN s.cost_points_snapshot ELSE 0 END), 0)
	+ COALESCE(SUM(CASE WHEN s.id IS NULL AND ga.source = 'local' AND ga.service_mode = 'purchase' THEN ga.cost_points_snapshot ELSE 0 END), 0) AS cost`).
		Joins("LEFT JOIN gmail_code_sessions AS s ON s.order_no = o.order_no").
		Joins("LEFT JOIN gmail_allocations AS ga ON ga.order_no = o.order_no").
		Where("o.product_type = ?", "gmail")
	if join != "" {
		query = query.Joins(join)
	}
	if err := query.Group(keyExpr + ", " + nameExpr).Order("order_count DESC, item_key ASC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load Gmail finance breakdown: %w", err)
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

func parseDecimalOrZero(value string) decimal.Decimal {
	parsed, err := money.Parse(strings.TrimSpace(value))
	if err != nil {
		return decimal.Zero
	}
	return parsed
}

func (s *Service) ListActivations(ctx context.Context, offset, limit int) ([]ActivationItem, int64, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int64
	if err := s.dbFor(ctx).Model(&sessionModel{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count Gmail activations: %w", err)
	}
	var items []ActivationItem
	if err := s.dbFor(ctx).Table("gmail_code_sessions AS s").
		Select(`s.id, s.order_no, o.project_id, p.name AS project_name, s.source, s.provider_service_code,
s.email, s.status, s.received_count, s.cost_points_snapshot AS cost_points, s.last_safe_error,
s.started_at, s.expires_at, s.completed_at, s.created_at`).
		Joins("JOIN orders AS o ON o.order_no = s.order_no").
		Joins("JOIN projects AS p ON p.id = o.project_id").
		Order("s.id DESC").Offset(offset).Limit(limit).Scan(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list Gmail activations: %w", err)
	}
	return items, total, nil
}

type priceChange struct {
	Code      string
	Name      string
	Previous  string
	Current   string
	ChangedAt time.Time
}

func (s *Service) Sync(ctx context.Context) error {
	apiKey := strings.TrimSpace(runtimeconfig.String("smsbower_api_key", ""))
	if apiKey == "" {
		return nil
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	balance, err := s.client.Balance(ctx, apiKey)
	if err != nil {
		return s.syncFailure(ctx, err)
	}
	services, err := s.client.Services(ctx, apiKey)
	if err != nil {
		return s.syncFailure(ctx, err)
	}
	prices, err := s.client.GmailPrices(ctx, apiKey)
	if err != nil {
		return s.syncFailure(ctx, err)
	}
	usedCodes := make(map[string]bool)
	var routedCodes []string
	if err := s.dbFor(ctx).Model(&routeModel{}).
		Where("source = ?", SourceSMSBower).
		Distinct("provider_service_code").Pluck("provider_service_code", &routedCodes).Error; err != nil {
		return fmt.Errorf("load routed SMSBower services: %w", err)
	}
	for _, code := range routedCodes {
		usedCodes[code] = true
	}
	missingRoutedPrices := make([]string, 0)
	for code := range usedCodes {
		if _, ok := prices[code]; !ok {
			missingRoutedPrices = append(missingRoutedPrices, code)
		}
	}
	if len(missingRoutedPrices) > 0 {
		sort.Strings(missingRoutedPrices)
		return s.syncFailure(ctx, fmt.Errorf("%w: routed Gmail prices missing for %s", ErrRemote, strings.Join(missingRoutedPrices, ",")))
	}
	changes := make([]priceChange, 0)
	var state accountStateModel
	threshold := parseDecimalOrZero(runtimeconfig.String("smsbower_balance_warning_threshold", "0"))
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "id = 1").Error; err != nil {
			return err
		}
		var existing []serviceModel
		if err := tx.Find(&existing).Error; err != nil {
			return err
		}
		byCode := make(map[string]serviceModel, len(existing))
		for _, item := range existing {
			byCode[item.Code] = item
		}
		if err := tx.Model(&serviceModel{}).Where("active = ?", true).Update("active", false).Error; err != nil {
			return err
		}
		for _, remote := range services {
			rest, supportsGmail := prices[remote.Code]
			if !supportsGmail {
				continue
			}
			priceValue := money.Format(rest.Price)
			stock := uint(max(rest.Count, 0))
			old, exists := byCode[remote.Code]
			changed := exists && parseDecimalOrZero(old.GmailPrice).Cmp(rest.Price) != 0
			notificationPending := changed
			if !notificationPending && exists && old.PriceChangedAt != nil {
				notificationPending = old.LastNotifiedPrice == nil || parseDecimalOrZero(*old.LastNotifiedPrice).Cmp(rest.Price) != 0
			}
			if notificationPending && usedCodes[remote.Code] {
				previous := old.GmailPrice
				changedAt := now
				if !changed {
					if old.PreviousPrice != nil {
						previous = *old.PreviousPrice
					}
					if old.PriceChangedAt != nil {
						changedAt = old.PriceChangedAt.UTC()
					}
				}
				changes = append(changes, priceChange{
					Code: remote.Code, Name: remote.Name, Previous: previous, Current: priceValue, ChangedAt: changedAt,
				})
			}
			if !exists {
				if err := tx.Create(&serviceModel{
					Code: remote.Code, Name: remote.Name, GmailPrice: priceValue, GmailStock: stock,
					Active: true, LastSeenAt: now,
				}).Error; err != nil {
					return err
				}
				continue
			}
			updates := map[string]any{
				"name": remote.Name, "gmail_price": priceValue, "gmail_stock": stock,
				"active": true, "last_seen_at": now,
			}
			if changed {
				updates["previous_price"] = old.GmailPrice
				updates["price_changed_at"] = now
			}
			if err := tx.Model(&serviceModel{}).Where("code = ?", remote.Code).Updates(updates).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{
			"balance": money.Format(balance), "health_status": "healthy", "consecutive_failures": 0,
			"last_safe_error": "", "last_synced_at": now, "last_success_at": now,
		}
		if state.FailureAlertActive {
			updates["failure_alert_active"] = false
			updates["generation"] = gorm.Expr("generation + 1")
		}
		if balance.GreaterThan(threshold) && state.BalanceAlertActive {
			updates["balance_alert_active"] = false
			updates["generation"] = gorm.Expr("generation + 1")
		}
		if err := tx.Model(&accountStateModel{}).Where("id = 1").Updates(updates).Error; err != nil {
			return err
		}
		return tx.First(&state, "id = 1").Error
	})
	if err != nil {
		return fmt.Errorf("persist SMSBower sync: %w", err)
	}
	if balance.LessThanOrEqual(threshold) && !state.BalanceAlertActive {
		alert := Alert{
			ID: fmt.Sprintf("smsbower-balance-%d", state.Generation), Subject: "SMSBower 上游余额不足",
			Body: fmt.Sprintf("SMSBower 当前余额为 %s，已低于或等于预警阈值 %s。请及时充值，以免 Gmail 上游履约停止。", money.Format(balance), money.Format(threshold)),
		}
		if err := s.notify(ctx, alert); err != nil {
			return err
		}
		if err := s.dbFor(ctx).Model(&accountStateModel{}).Where("id = 1 AND balance_alert_active = ?", false).Update("balance_alert_active", true).Error; err != nil {
			return fmt.Errorf("mark SMSBower balance alert: %w", err)
		}
	}
	if len(changes) > 0 {
		sort.Slice(changes, func(i, j int) bool { return changes[i].Code < changes[j].Code })
		lines := make([]string, len(changes))
		parts := make([]string, len(changes))
		for i := range changes {
			lines[i] = fmt.Sprintf("%s（%s）：%s → %s", changes[i].Name, changes[i].Code, changes[i].Previous, changes[i].Current)
			parts[i] = changes[i].Code + ":" + changes[i].Current + ":" + changes[i].ChangedAt.UTC().Format(time.RFC3339Nano)
		}
		alert := Alert{
			ID: "smsbower-price-" + stableDigest(strings.Join(parts, "|")), Subject: "SMSBower Gmail 上游价格变动",
			Body: "以下已映射服务的 Gmail 价格发生变化：\n" + strings.Join(lines, "\n") + "\n系统会继续按最低毛利率拦截亏损订单，请检查项目售价。",
		}
		if err := s.notify(ctx, alert); err != nil {
			return err
		}
		for _, change := range changes {
			if err := s.dbFor(ctx).Model(&serviceModel{}).
				Where("code = ? AND gmail_price = ? AND price_changed_at = ?", change.Code, change.Current, change.ChangedAt).
				Update("last_notified_price", change.Current).Error; err != nil {
				return fmt.Errorf("mark SMSBower price notification: %w", err)
			}
		}
	}
	return nil
}

func (s *Service) syncFailure(ctx context.Context, cause error) error {
	now := s.now()
	safe := safeUpstreamError(cause)
	health := "degraded"
	if errors.Is(cause, ErrBadKey) {
		health = "unavailable"
	}
	var state accountStateModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&accountStateModel{}).Where("id = 1").Updates(map[string]any{
			"health_status": health, "consecutive_failures": gorm.Expr("consecutive_failures + 1"),
			"last_safe_error": safe, "last_synced_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.First(&state, "id = 1").Error
	})
	if err != nil {
		return fmt.Errorf("persist SMSBower sync failure: %w", err)
	}
	if (errors.Is(cause, ErrBadKey) || state.ConsecutiveFailures >= 3) && !state.FailureAlertActive {
		alert := Alert{
			ID: fmt.Sprintf("smsbower-failure-%d", state.Generation), Subject: "SMSBower 上游连接异常",
			Body: fmt.Sprintf("SMSBower 同步失败：%s。当前连续失败 %d 次，请检查 API Key 和上游服务状态。", safe, state.ConsecutiveFailures),
		}
		if notifyErr := s.notify(ctx, alert); notifyErr != nil {
			return errors.Join(cause, notifyErr)
		}
		if err := s.dbFor(ctx).Model(&accountStateModel{}).Where("id = 1 AND failure_alert_active = ?", false).Update("failure_alert_active", true).Error; err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}

func (s *Service) notify(ctx context.Context, alert Alert) error {
	if s.notifier == nil {
		return nil
	}
	if err := s.notifier.NotifySMSBower(context.WithoutCancel(ctx), alert); err != nil {
		return fmt.Errorf("send SMSBower alert: %w", err)
	}
	return nil
}

func safeUpstreamError(err error) string {
	switch {
	case errors.Is(err, ErrBadKey):
		return "API Key 无效"
	case errors.Is(err, ErrInsufficientBalance):
		return "上游余额不足"
	case errors.Is(err, ErrNoMail):
		return "上游暂无可用 Gmail"
	case errors.Is(err, ErrPriceChanged):
		return "上游价格已变化"
	case errors.Is(err, ErrCodeWaiting):
		return "等待验证码"
	case remoteActionFinal(err):
		return "上游激活已结束"
	default:
		return "上游网络或响应异常"
	}
}

func stableDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type sessionTaskPayload struct {
	SessionID uint `json:"sessionId"`
}

func (s *Service) ScheduleProvision(ctx context.Context, sessionID uint) error {
	if sessionID == 0 {
		return ErrSessionMissing
	}
	if s.queue == nil {
		return errors.New("gmail: task queue unavailable")
	}
	payload, _ := json.Marshal(sessionTaskPayload{SessionID: sessionID})
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailProvision, payload),
		asynq.Queue(platform.QueueDefault), asynq.Unique(time.Minute), asynq.MaxRetry(0),
		asynq.Timeout(30*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) schedulePoll(ctx context.Context, sessionID uint) error {
	if sessionID == 0 || s.queue == nil {
		return errors.New("gmail: task queue unavailable")
	}
	payload, _ := json.Marshal(sessionTaskPayload{SessionID: sessionID})
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailPoll, payload),
		asynq.Queue(platform.QueueMailfetch), asynq.Unique(4*time.Second), asynq.MaxRetry(2),
		asynq.Timeout(20*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) DispatchDueSessions(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := s.now()
	var provisionIDs []uint
	if err := s.dbFor(ctx).Model(&sessionModel{}).
		Where("status = ? OR (status IN ? AND next_poll_at IS NOT NULL AND next_poll_at <= ?)",
			SessionPending, []string{SessionProvisioning, SessionFailed, SessionUnknown}, now).
		Order("id ASC").Limit(limit).Pluck("id", &provisionIDs).Error; err != nil {
		return 0, fmt.Errorf("list Gmail provision sessions: %w", err)
	}
	queued := 0
	for _, id := range provisionIDs {
		if err := s.ScheduleProvision(ctx, id); err != nil {
			return queued, err
		}
		queued++
	}
	remaining := max(limit-len(provisionIDs), 0)
	if remaining == 0 {
		return queued, nil
	}
	var pollIDs []uint
	if err := s.dbFor(ctx).Model(&sessionModel{}).
		Where("status IN ? AND next_poll_at IS NOT NULL AND next_poll_at <= ?",
			[]string{SessionActive, SessionCompleting, SessionCancelling, SessionCompleted, SessionCancelled}, now).
		Order("id ASC").Limit(remaining).Pluck("id", &pollIDs).Error; err != nil {
		return queued, fmt.Errorf("list Gmail poll sessions: %w", err)
	}
	for _, id := range pollIDs {
		if err := s.schedulePoll(ctx, id); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

func (s *Service) Provision(ctx context.Context, sessionID uint) error {
	session, claimed, err := s.claimProvision(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.Source == SourceLocal {
		switch session.Status {
		case SessionActive:
			if err := s.ensureTradeActivation(ctx, *session); err != nil {
				return err
			}
			return s.schedulePoll(context.WithoutCancel(ctx), session.ID)
		case SessionCompleted, SessionCancelled, SessionFailed:
			return s.finishLocalSession(ctx, *session)
		default:
			return nil
		}
	}
	switch session.Status {
	case SessionActive:
		if err := s.ensureTradeActivation(ctx, *session); err != nil {
			return err
		}
		return s.schedulePoll(context.WithoutCancel(ctx), session.ID)
	case SessionFailed, SessionUnknown:
		if s.trade == nil {
			return errors.New("gmail: trade callback unavailable")
		}
		if err := s.trade.FailGmailOrder(ctx, session.OrderNo, session.LastSafeError); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, session.ID)
	case SessionCompleted, SessionCancelled:
		return nil
	}
	if !claimed {
		return s.failProvision(ctx, *session, SessionUnknown, "采购请求状态不确定，系统已停止自动重试并退款。", true)
	}
	apiKey := strings.TrimSpace(runtimeconfig.String("smsbower_api_key", ""))
	maxPrice, err := money.Parse(session.MaxPriceSnapshot)
	if apiKey == "" || err != nil {
		return s.failProvision(ctx, *session, SessionFailed, "SMSBower 配置不可用，Gmail 采购失败并退款。", false)
	}
	activation, err := s.client.Activate(ctx, apiKey, session.ProviderServiceCode, maxPrice)
	if err != nil {
		explicit := errors.Is(err, ErrBadKey) || errors.Is(err, ErrNoMail) || errors.Is(err, ErrInsufficientBalance) || errors.Is(err, ErrPriceChanged)
		if explicit {
			return s.failProvision(ctx, *session, SessionFailed, safeUpstreamError(err)+"，Gmail 采购失败并退款。", false)
		}
		return s.failProvision(ctx, *session, SessionUnknown, "采购结果不确定，系统未重复采购并已退款，请管理员人工核对上游。", true)
	}
	now := s.now()
	expiresAt := now.Add(gmailLifetime)
	updates := map[string]any{
		"source_ref": strconv.FormatUint(activation.MailID, 10), "email": activation.Email,
		"status": SessionActive, "started_at": now, "expires_at": expiresAt,
		"next_poll_at": now, "last_safe_error": "", "version": gorm.Expr("version + 1"),
	}
	result := s.dbFor(ctx).Model(&sessionModel{}).
		Where("id = ? AND status = ?", session.ID, SessionProvisioning).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("activate Gmail session: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("gmail: provision session state conflict")
	}
	session.SourceRef = strconv.FormatUint(activation.MailID, 10)
	session.Email = activation.Email
	session.Status = SessionActive
	session.StartedAt = &now
	session.ExpiresAt = &expiresAt
	if err := s.ensureTradeActivation(ctx, *session); err != nil {
		_ = s.schedulePoll(context.WithoutCancel(ctx), session.ID)
		return err
	}
	return s.schedulePoll(context.WithoutCancel(ctx), session.ID)
}

func (s *Service) claimProvision(ctx context.Context, sessionID uint) (*sessionModel, bool, error) {
	var session sessionModel
	claimed := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, sessionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSessionMissing
			}
			return err
		}
		if session.Status != SessionPending {
			return nil
		}
		recoverAt := s.now().Add(gmailProvisionLease)
		if err := tx.Model(&sessionModel{}).Where("id = ? AND status = ?", session.ID, SessionPending).
			Updates(map[string]any{"status": SessionProvisioning, "next_poll_at": recoverAt, "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
		session.Status = SessionProvisioning
		session.NextPollAt = &recoverAt
		claimed = true
		return nil
	})
	return &session, claimed, err
}

func (s *Service) failProvision(ctx context.Context, session sessionModel, status, safeMessage string, uncertain bool) error {
	now := s.now()
	result := s.dbFor(ctx).Model(&sessionModel{}).Where("id = ? AND status = ?", session.ID, SessionProvisioning).Updates(map[string]any{
		"status": status, "last_safe_error": safeMessage, "completed_at": now, "next_poll_at": now,
		"version": gorm.Expr("version + 1"),
	})
	if result.Error != nil {
		return fmt.Errorf("fail Gmail provision: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("gmail: provision failure state conflict")
	}
	if s.trade == nil {
		return errors.New("gmail: trade callback unavailable")
	}
	callbackErr := s.trade.FailGmailOrder(ctx, session.OrderNo, safeMessage)
	if callbackErr == nil {
		_ = s.clearNextPoll(ctx, session.ID)
	}
	if uncertain {
		alertErr := s.notify(ctx, Alert{
			ID: "smsbower-uncertain-" + stableDigest(session.OrderNo), Subject: "SMSBower Gmail 采购结果待人工核对",
			Body: fmt.Sprintf("订单 %s 的 SMSBower 采购结果不确定。系统没有自动重复采购，用户款项已按失败流程退款；请在上游后台核对是否产生了激活和成本。", session.OrderNo),
		})
		return errors.Join(callbackErr, alertErr)
	}
	return callbackErr
}

func (s *Service) ensureTradeActivation(ctx context.Context, session sessionModel) error {
	if s.trade == nil || session.StartedAt == nil || session.ExpiresAt == nil || session.Email == "" {
		return errors.New("gmail: activation callback unavailable")
	}
	allocationID, err := s.ensureCodeAllocation(ctx, session)
	if err != nil {
		return err
	}
	return s.trade.ActivateGmailOrder(ctx, tradeapp.ActivateGmailOrderRequest{
		OrderNo: session.OrderNo, AllocationID: allocationID, SessionID: session.ID, Email: session.Email,
		StartedAt: session.StartedAt.UTC(), ExpiresAt: session.ExpiresAt.UTC(),
	})
}

func (s *Service) ensureCodeAllocation(ctx context.Context, session sessionModel) (uint, error) {
	if session.ID == 0 || strings.TrimSpace(session.OrderNo) == "" || strings.TrimSpace(session.Email) == "" ||
		session.ServiceMode != string(tradedomain.ServiceModeCode) {
		return 0, ErrInvalidRoute
	}
	var model allocationModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if session.Source == SourceLocal {
			if err := tx.Where("order_no = ?", session.OrderNo).Take(&model).Error; err != nil {
				return fmt.Errorf("load local Gmail code allocation: %w", err)
			}
			if model.ResourceID == nil || model.Source != SourceLocal || model.ServiceMode != string(tradedomain.ServiceModeCode) ||
				model.GuardType != "gmail" || model.ProjectID == 0 || model.ProductID == 0 ||
				(model.SupplyScope != AllocationSupplyOwned && model.SupplyScope != AllocationSupplyPublic) ||
				!isGmailMailbox(model.Mailbox) || model.Status != AllocationStatusAllocated ||
				!strings.EqualFold(model.Email, session.Email) {
				return errors.New("gmail: allocation conflict")
			}
			if sourceID, err := strconv.ParseUint(session.SourceRef, 10, 64); err != nil || sourceID != uint64(model.ID) {
				return errors.New("gmail: allocation conflict")
			}
			return nil
		}
		if session.Source != SourceSMSBower {
			return ErrInvalidRoute
		}

		var order struct {
			ProjectID uint   `gorm:"column:project_id"`
			ProductID uint   `gorm:"column:project_product_id"`
			Product   string `gorm:"column:product_type"`
			Mode      string `gorm:"column:service_mode"`
		}
		if err := tx.Table("orders").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("project_id, project_product_id, product_type, service_mode").
			Where("order_no = ?", session.OrderNo).Take(&order).Error; err != nil {
			return fmt.Errorf("load Gmail allocation order: %w", err)
		}
		if order.ProjectID == 0 || order.ProductID == 0 || order.Product != "gmail" || order.Mode != string(tradedomain.ServiceModeCode) {
			return errors.New("gmail: allocation order conflict")
		}

		err := tx.Where("order_no = ?", session.OrderNo).Take(&model).Error
		if err == nil {
			if model.ResourceID != nil || model.Source != SourceSMSBower ||
				model.SourceRef != strconv.FormatUint(uint64(session.ID), 10) ||
				model.ServiceMode != string(tradedomain.ServiceModeCode) || model.GuardType != "gmail" ||
				model.ProjectID != order.ProjectID || model.ProductID != order.ProductID ||
				model.SupplyScope != AllocationSupplyPublic || model.Status != AllocationStatusAllocated ||
				!strings.EqualFold(model.Email, session.Email) {
				return errors.New("gmail: allocation conflict")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load Gmail code allocation: %w", err)
		}

		createdAt := s.now().UTC()
		guard := localAllocationGuardModel{OrderNo: session.OrderNo, Type: "gmail", CreatedAt: createdAt}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
			return fmt.Errorf("create Gmail allocation guard: %w", err)
		}
		var storedGuard localAllocationGuardModel
		if err := tx.Where("order_no = ?", session.OrderNo).Take(&storedGuard).Error; err != nil {
			return fmt.Errorf("load Gmail allocation guard: %w", err)
		}
		if storedGuard.Type != "gmail" {
			return errors.New("gmail: allocation guard conflict")
		}
		model = allocationModel{
			OrderNo: session.OrderNo, GuardType: "gmail", ProjectID: order.ProjectID, ProductID: order.ProductID,
			Source: SourceSMSBower, SourceRef: strconv.FormatUint(uint64(session.ID), 10),
			ServiceMode: string(tradedomain.ServiceModeCode), SupplyScope: AllocationSupplyPublic,
			Email: session.Email, Status: AllocationStatusAllocated,
			CostPointsSnapshot: session.CostPointsSnapshot, CreatedAt: createdAt,
		}
		if err := tx.Create(&model).Error; err != nil {
			return fmt.Errorf("create Gmail code allocation: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return model.ID, nil
}

func (s *Service) Poll(ctx context.Context, sessionID uint) error {
	var session sessionModel
	if err := s.dbFor(ctx).First(&session, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionMissing
		}
		return err
	}
	if session.Source == SourceLocal {
		return s.pollLocalSession(ctx, session)
	}
	switch session.Status {
	case SessionCompleted:
		if s.trade == nil {
			return errors.New("gmail: trade callback unavailable")
		}
		if err := s.trade.CompleteGmailOrder(ctx, session.OrderNo, gmailCompletionReason(session)); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, session.ID)
	case SessionCancelled:
		if s.trade == nil {
			return errors.New("gmail: trade callback unavailable")
		}
		if err := s.trade.FailGmailOrder(ctx, session.OrderNo, "Gmail 在 24 小时内未收到验证码，订单已退款。"); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, session.ID)
	case SessionUnknown:
		if s.trade == nil {
			return errors.New("gmail: trade callback unavailable")
		}
		if err := s.trade.FailGmailOrder(ctx, session.OrderNo, session.LastSafeError); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, session.ID)
	case SessionFailed, SessionPending, SessionProvisioning:
		return nil
	}
	if session.Status == SessionActive {
		if err := s.ensureTradeActivation(ctx, session); err != nil {
			return err
		}
	}
	if session.PendingRemoteAction != "" {
		return s.applyRemoteAction(ctx, session)
	}
	if session.Status != SessionActive {
		return nil
	}
	now := s.now()
	if session.ExpiresAt != nil && !now.Before(session.ExpiresAt.UTC()) {
		updated, err := s.prepareExpiryAction(ctx, session.ID)
		if err != nil {
			return err
		}
		return s.applyRemoteAction(ctx, *updated)
	}
	mailID, err := strconv.ParseUint(session.SourceRef, 10, 64)
	apiKey := strings.TrimSpace(runtimeconfig.String("smsbower_api_key", ""))
	if err != nil || mailID == 0 || apiKey == "" {
		return s.deferPoll(ctx, session.ID, "SMSBower 激活信息不可用", ErrRemote)
	}
	code, err := s.client.Code(ctx, apiKey, mailID)
	if errors.Is(err, ErrCodeWaiting) {
		return s.deferPoll(ctx, session.ID, "", nil)
	}
	if remoteActionFinal(err) {
		updated, prepareErr := s.prepareExpiryAction(ctx, session.ID)
		if prepareErr != nil {
			return prepareErr
		}
		return s.applyRemoteAction(ctx, *updated)
	}
	if err != nil {
		return s.deferPoll(ctx, session.ID, safeUpstreamError(err), err)
	}
	updated, err := s.recordCode(ctx, session.ID, code)
	if err != nil {
		return err
	}
	return s.applyRemoteAction(ctx, *updated)
}

func (s *Service) prepareExpiryAction(ctx context.Context, sessionID uint) (*sessionModel, error) {
	var session sessionModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, sessionID).Error; err != nil {
			return err
		}
		if session.Status != SessionActive || session.PendingRemoteAction != "" {
			return nil
		}
		action, status := ActionComplete, SessionCompleting
		if session.ReceivedCount == 0 {
			action, status = ActionCancel, SessionCancelling
		}
		if err := tx.Model(&sessionModel{}).Where("id = ?", session.ID).Updates(map[string]any{
			"status": status, "pending_remote_action": action, "next_poll_at": s.now(), "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
			return err
		}
		session.Status, session.PendingRemoteAction = status, action
		return nil
	})
	return &session, err
}

func (s *Service) recordCode(ctx context.Context, sessionID uint, value string) (*sessionModel, error) {
	var session sessionModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, sessionID).Error; err != nil {
			return err
		}
		if session.Status != SessionActive || session.PendingRemoteAction != "" || session.ReceivedCount >= MaxCodes {
			return errors.New("gmail: code session state conflict")
		}
		codes, err := decodeCodes(session.CodesJSON)
		if err != nil || len(codes) != int(session.ReceivedCount) {
			return errors.New("gmail: code session count mismatch")
		}
		now := s.now()
		count := int(session.ReceivedCount) + 1
		codes = append(codes, Code{Seq: count, Code: value, ReceivedAt: now})
		payload, err := json.Marshal(codes)
		if err != nil {
			return err
		}
		action, status := nextCodeAction(count)
		if err := tx.Model(&sessionModel{}).Where("id = ?", session.ID).Updates(map[string]any{
			"codes_json": payload, "received_count": count, "pending_remote_action": action,
			"status": status, "next_poll_at": now, "last_safe_error": "", "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
			return err
		}
		session.CodesJSON, session.ReceivedCount = payload, uint8(count)
		session.PendingRemoteAction, session.Status = action, status
		return nil
	})
	return &session, err
}

func nextCodeAction(count int) (action, status string) {
	if count >= MaxCodes {
		return ActionComplete, SessionCompleting
	}
	return ActionWaitNext, SessionActive
}

func (s *Service) applyRemoteAction(ctx context.Context, session sessionModel) error {
	mailID, err := strconv.ParseUint(session.SourceRef, 10, 64)
	apiKey := strings.TrimSpace(runtimeconfig.String("smsbower_api_key", ""))
	if err != nil || mailID == 0 || apiKey == "" {
		return s.deferPoll(ctx, session.ID, "SMSBower 激活信息不可用", ErrRemote)
	}
	status := 0
	switch session.PendingRemoteAction {
	case ActionWaitNext:
		status = 5
	case ActionComplete:
		status = 3
	case ActionCancel:
		status = 2
	default:
		return errors.New("gmail: invalid pending remote action")
	}
	remoteErr := s.client.SetStatus(ctx, apiKey, mailID, status)
	if remoteErr != nil && !remoteActionFinal(remoteErr) {
		return s.deferPoll(ctx, session.ID, safeUpstreamError(remoteErr), remoteErr)
	}
	now := s.now()
	uncertainCancel := session.PendingRemoteAction == ActionCancel && errors.Is(remoteErr, ErrActivationStatus)
	cancelReason := "Gmail 在 24 小时内未收到验证码，订单已退款。"
	if uncertainCancel {
		cancelReason = "SMSBower 取消结果不确定，订单已进入退款流程，请管理员核对上游是否产生费用。"
	}
	updates := map[string]any{
		"pending_remote_action": "", "last_safe_error": "", "version": gorm.Expr("version + 1"),
	}
	switch session.PendingRemoteAction {
	case ActionWaitNext:
		updates["status"] = SessionActive
		updates["next_poll_at"] = now.Add(gmailPollInterval)
	case ActionComplete:
		updates["status"] = SessionCompleted
		updates["completed_at"] = now
		updates["next_poll_at"] = now
	case ActionCancel:
		updates["status"] = SessionCancelled
		if uncertainCancel {
			updates["status"] = SessionUnknown
			updates["last_safe_error"] = cancelReason
		}
		updates["completed_at"] = now
		updates["next_poll_at"] = now
	}
	result := s.dbFor(ctx).Model(&sessionModel{}).
		Where("id = ? AND pending_remote_action = ?", session.ID, session.PendingRemoteAction).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("finish Gmail remote action: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("gmail: remote action state conflict")
	}
	switch session.PendingRemoteAction {
	case ActionComplete:
		if s.trade == nil {
			return errors.New("gmail: trade callback unavailable")
		}
		if err := s.trade.CompleteGmailOrder(ctx, session.OrderNo, gmailCompletionReason(session)); err != nil {
			return err
		}
		return s.clearNextPoll(ctx, session.ID)
	case ActionCancel:
		var callbackErr error
		if s.trade == nil {
			callbackErr = errors.New("gmail: trade callback unavailable")
		} else if callbackErr = s.trade.FailGmailOrder(ctx, session.OrderNo, cancelReason); callbackErr == nil {
			callbackErr = s.clearNextPoll(ctx, session.ID)
		}
		if !uncertainCancel {
			return callbackErr
		}
		alertErr := s.notify(ctx, Alert{
			ID: "smsbower-cancel-uncertain-" + stableDigest(session.OrderNo), Subject: "SMSBower Gmail 取消结果待人工核对",
			Body: fmt.Sprintf("订单 %s 的 SMSBower Gmail 取消结果不确定。系统已将成本归入未知并进入用户退款流程；请在上游后台核对激活状态和实际扣费。", session.OrderNo),
		})
		return errors.Join(callbackErr, alertErr)
	default:
		return nil
	}
}

func gmailCompletionReason(session sessionModel) string {
	if session.ReceivedCount >= MaxCodes {
		return "Gmail 已接收 3 个验证码，接码会话完成。"
	}
	return fmt.Sprintf("Gmail 24 小时接码窗口结束，共接收 %d 个验证码。", session.ReceivedCount)
}

func (s *Service) deferPoll(ctx context.Context, sessionID uint, safeMessage string, cause error) error {
	updates := map[string]any{"next_poll_at": s.now().Add(gmailPollInterval)}
	if safeMessage != "" {
		updates["last_safe_error"] = safeMessage
	}
	if err := s.dbFor(ctx).Model(&sessionModel{}).Where("id = ?", sessionID).Updates(updates).Error; err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func remoteActionFinal(err error) bool {
	return errors.Is(err, ErrActivationMissing) || errors.Is(err, ErrActivationStatus)
}

func (s *Service) clearNextPoll(ctx context.Context, sessionID uint) error {
	return s.dbFor(ctx).Model(&sessionModel{}).Where("id = ?", sessionID).Update("next_poll_at", nil).Error
}
