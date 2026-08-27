package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	moneyfmt "github.com/donnel666/remail/internal/money"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/donnel666/remail/internal/trade/infra"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type Module struct {
	UseCase       *tradeapp.UseCase
	OperationLogs governanceapp.OperationLogPort
}

func NewModule(db *gorm.DB, coreProjects *coreapp.ProjectUseCase, billingWallet *billingapp.WalletUseCase, alloc *allocapp.UseCase, tokens *openapiapp.UseCase, redisClients ...redis.UniversalClient) *Module {
	var redisClient redis.UniversalClient
	if len(redisClients) > 0 {
		redisClient = redisClients[0]
	}
	repo := infra.NewRepo(db)
	systemLogs := governanceinfra.NewSystemLogRepo(db)
	operationLogs := governanceinfra.NewOperationLogRepo(db)
	uc := tradeapp.NewUseCase(
		repo,
		coreOrderingAdapter{projects: coreProjects, db: db, redis: redisClient},
		billingWalletAdapter{wallet: billingWallet},
		allocationAdapter{alloc: alloc},
		orderTokenAdapter{tokens: tokens},
	)
	uc.SetOrderDeliveryPort(orderDeliveryAdapter{db: db})
	uc.SetProjectDisplayPort(projectDisplayAdapter{db: db})
	uc.SetSystemLogPort(systemLogs)
	return &Module{
		UseCase:       uc,
		OperationLogs: operationLogs,
	}
}

// projectDisplayAdapter resolves the current project presentation fields for
// one order page in a single query, mirroring orderDeliveryAdapter's approach.
type projectDisplayAdapter struct {
	db *gorm.DB
}

func (a projectDisplayAdapter) ProjectDisplays(ctx context.Context, projectIDs []uint) (map[uint]tradeapp.ProjectDisplay, error) {
	result := make(map[uint]tradeapp.ProjectDisplay, len(projectIDs))
	if len(projectIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ID      uint   `gorm:"column:id"`
		Name    string `gorm:"column:name"`
		LogoURL string `gorm:"column:logo_url"`
	}
	if err := a.db.WithContext(ctx).
		Table("projects").
		Select("id, name, logo_url").
		Where("id IN ?", projectIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = tradeapp.ProjectDisplay{
			Name:    row.Name,
			LogoURL: strings.TrimSpace(row.LogoURL),
		}
	}
	return result, nil
}

type orderDeliveryAdapter struct {
	db *gorm.DB
}

func (a orderDeliveryAdapter) FindOrderDelivery(ctx context.Context, orderID uint) (*tradeapp.OrderDeliverySummary, error) {
	items, err := a.ListOrderDeliveries(ctx, []uint{orderID})
	if err != nil {
		return nil, err
	}
	delivery, ok := items[orderID]
	if !ok {
		return nil, nil
	}
	return &delivery, nil
}

func (a orderDeliveryAdapter) ListOrderDeliveries(ctx context.Context, orderIDs []uint) (map[uint]tradeapp.OrderDeliverySummary, error) {
	result := make(map[uint]tradeapp.OrderDeliverySummary)
	if len(orderIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		OrderID          uint      `gorm:"column:order_id"`
		VerificationCode string    `gorm:"column:verification_code"`
		ReceivedAt       time.Time `gorm:"column:received_at"`
	}
	db := a.db
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx
	}
	if err := db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Select(`h.order_id,
COALESCE(CASE WHEN mp.message_id IS NULL THEN m.verification_code ELSE mp.verification_code END, '') AS verification_code,
h.message_received_at AS received_at`).
		Joins("LEFT JOIN mailmatch_messages AS m ON m.id = h.message_id").
		Joins("LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id").
		Where("h.order_id IN ?", orderIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.OrderID] = tradeapp.OrderDeliverySummary{
			VerificationCode: row.VerificationCode,
			ReceivedAt:       row.ReceivedAt,
		}
	}
	return result, nil
}

func (a orderDeliveryAdapter) ListPendingNotifications(ctx context.Context, afterOrderID uint, limit int) ([]tradeapp.OrderDeliveryNotification, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []struct {
		OrderID    uint      `gorm:"column:order_id"`
		OrderNo    string    `gorm:"column:order_no"`
		ReceivedAt time.Time `gorm:"column:received_at"`
	}
	query := a.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Select("h.order_id, o.order_no, h.message_received_at AS received_at").
		Joins("JOIN orders AS o ON o.id = h.order_id").
		Where(`
			(o.service_mode = 'code' AND o.status = 'active')
			OR (
				o.service_mode = 'purchase'
				AND o.status IN ('active', 'completed')
				AND o.activated_at IS NULL
				AND (o.receive_until IS NULL OR h.message_received_at <= o.receive_until)
			)
		`)
	if afterOrderID > 0 {
		query = query.Where("h.order_id > ?", afterOrderID)
	}
	if err := query.
		Order("h.order_id ASC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 && afterOrderID > 0 {
		return a.ListPendingNotifications(ctx, 0, limit)
	}
	items := make([]tradeapp.OrderDeliveryNotification, len(rows))
	for i := range rows {
		items[i] = tradeapp.OrderDeliveryNotification{
			OrderID:    rows[i].OrderID,
			OrderNo:    rows[i].OrderNo,
			ReceivedAt: rows[i].ReceivedAt,
		}
	}
	return items, nil
}

type coreOrderingAdapter struct {
	projects *coreapp.ProjectUseCase
	db       *gorm.DB
	redis    redis.UniversalClient
}

func (a coreOrderingAdapter) GetOrderingQuote(ctx context.Context, projectID uint, productID uint, buyerUserID uint, serviceMode domain.ServiceMode) (*tradeapp.OrderingQuote, error) {
	quote, err := a.projects.GetOrderingQuote(ctx, projectID, productID, buyerUserID, string(serviceMode))
	if err != nil {
		return nil, mapCoreError(err)
	}
	result := &tradeapp.OrderingQuote{
		ProjectID:               quote.ProjectID,
		ProductID:               quote.ProductID,
		ProductType:             domain.ProductType(quote.ProductType),
		PayAmount:               quote.PayAmount,
		CodeWindowMinutes:       quote.CodeWindowMinutes,
		ActivationWindowMinutes: quote.ActivationWindowMinutes,
		WarrantyMinutes:         quote.WarrantyMinutes,
	}
	ratio, err := a.userPriceDiscountRatio(ctx, buyerUserID)
	if err != nil {
		return nil, err
	}
	if err := applyOrderingDiscount(result, ratio); err != nil {
		return nil, err
	}
	return result, nil
}

func (a coreOrderingAdapter) GetOrderingQuoteByType(ctx context.Context, projectID uint, productType domain.ProductType, buyerUserID uint, serviceMode domain.ServiceMode) (*tradeapp.OrderingQuote, error) {
	productID, cached, err := a.projectProductID(ctx, projectID, productType)
	if err != nil {
		return nil, err
	}
	if productID == 0 {
		return nil, domain.ErrProjectUnavailable
	}
	quote, err := a.GetOrderingQuote(ctx, projectID, productID, buyerUserID, serviceMode)
	if err == nil && quote.ProductType == productType {
		return quote, nil
	}
	if !cached {
		if err == nil {
			return nil, domain.ErrProjectUnavailable
		}
		return nil, err
	}
	if err != nil && !errors.Is(err, domain.ErrProjectUnavailable) {
		return nil, err
	}

	databaseProductID, dbErr := a.projectProductIDFromDB(ctx, projectID, productType)
	if dbErr != nil {
		return nil, dbErr
	}
	if databaseProductID == 0 {
		_ = a.redis.HDel(ctx, projectProductRedisKey(projectID), string(productType)).Err()
		return nil, domain.ErrProjectUnavailable
	}
	if databaseProductID == productID {
		if err != nil {
			return nil, err
		}
		return nil, domain.ErrProjectUnavailable
	}
	quote, retryErr := a.GetOrderingQuote(ctx, projectID, databaseProductID, buyerUserID, serviceMode)
	if retryErr != nil {
		return nil, retryErr
	}
	if quote.ProductType != productType {
		return nil, domain.ErrProjectUnavailable
	}
	return quote, nil
}

func (a coreOrderingAdapter) projectProductID(ctx context.Context, projectID uint, productType domain.ProductType) (uint, bool, error) {
	if projectID == 0 || !isCheckoutProductType(productType) {
		return 0, false, nil
	}
	if a.redis != nil {
		value, err := a.redis.HGet(ctx, projectProductRedisKey(projectID), string(productType)).Result()
		if err == nil {
			if id, parseErr := strconv.ParseUint(value, 10, 64); parseErr == nil && id > 0 {
				return uint(id), true, nil
			}
			_ = a.redis.HDel(ctx, projectProductRedisKey(projectID), string(productType)).Err()
		}
	}
	productID, err := a.projectProductIDFromDB(ctx, projectID, productType)
	return productID, false, err
}

func (a coreOrderingAdapter) projectProductIDFromDB(ctx context.Context, projectID uint, productType domain.ProductType) (uint, error) {
	if projectID == 0 || !isCheckoutProductType(productType) {
		return 0, nil
	}
	var row struct {
		ID uint `gorm:"column:id"`
	}
	db := a.db
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx
	}
	err := db.WithContext(ctx).
		Table("project_products").
		Select("id").
		Where("project_id = ? AND type = ?", projectID, string(productType)).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load project product ID: %w", err)
	}
	if row.ID == 0 {
		return 0, nil
	}
	if a.redis != nil {
		_ = a.redis.HSet(ctx, projectProductRedisKey(projectID), string(productType), row.ID).Err()
	}
	return row.ID, nil
}

func isCheckoutProductType(productType domain.ProductType) bool {
	switch productType {
	case domain.ProductTypeMicrosoft, domain.ProductTypeDomain, domain.ProductTypeGmail, domain.ProductTypeGmailVariant, domain.ProductTypeICloud:
		return true
	default:
		return false
	}
}

func projectProductRedisKey(projectID uint) string {
	return "trade:project-products:v1:" + strconv.FormatUint(uint64(projectID), 10)
}

func (a coreOrderingAdapter) userPriceDiscountRatio(ctx context.Context, userID uint) (string, error) {
	db := a.db
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx
	}
	var row struct {
		PriceDiscountRatio string `gorm:"column:price_discount_ratio"`
	}
	if err := db.WithContext(ctx).
		Table("users AS u").
		Select("COALESCE(user_group.price_discount_ratio, 1.000000) AS price_discount_ratio").
		Joins("LEFT JOIN user_groups AS user_group ON user_group.id = u.user_group_id").
		Where("u.id = ?", userID).
		Take(&row).Error; err != nil {
		return "", fmt.Errorf("load user price discount: %w", err)
	}
	return row.PriceDiscountRatio, nil
}

func applyOrderingDiscount(quote *tradeapp.OrderingQuote, groupRatio string) error {
	ratio := decimal.NewFromInt(1)
	for _, ratioValue := range []string{groupRatio, runtimeconfig.ProductPriceMultiplier(string(quote.ProductType))} {
		candidate, err := moneyfmt.Parse(ratioValue)
		if err != nil || candidate.IsNegative() || candidate.GreaterThan(decimal.NewFromInt(1)) {
			return fmt.Errorf("invalid price discount ratio")
		}
		if candidate.LessThan(ratio) {
			ratio = candidate
		}
	}
	value, err := moneyfmt.Parse(quote.PayAmount)
	if err != nil {
		return fmt.Errorf("discount order amount: %w", err)
	}
	quote.PayAmount = moneyfmt.Format(value.Mul(ratio))
	return nil
}

type billingWalletAdapter struct {
	wallet *billingapp.WalletUseCase
}

func (a billingWalletAdapter) ConsumerBalance(ctx context.Context, userID uint) (string, error) {
	balances, err := a.wallet.ListConsumerBalances(ctx, []uint{userID})
	if err != nil {
		return "", err
	}
	balance, exists := balances[userID]
	if !exists {
		return "0.00", nil
	}
	return balance, nil
}

func (a billingWalletAdapter) LockConsumer(ctx context.Context, userID uint) error {
	return a.wallet.LockConsumer(ctx, userID)
}

func (a billingWalletAdapter) DebitConsumer(ctx context.Context, cmd tradeapp.WalletCommand) (*tradeapp.WalletTransaction, error) {
	result, err := a.wallet.DebitConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID:         cmd.UserID,
		Amount:         cmd.Amount,
		Reason:         cmd.Reason,
		IdempotencyKey: cmd.IdempotencyKey,
		RequestID:      cmd.RequestID,
	})
	if err != nil {
		return nil, mapBillingError(err)
	}
	return &tradeapp.WalletTransaction{ID: result.Transaction.ID}, nil
}

func (a billingWalletAdapter) RecordHistoricalZeroDebit(ctx context.Context, cmd tradeapp.WalletCommand) (*tradeapp.WalletTransaction, error) {
	result, err := a.wallet.RecordHistoricalZeroDebit(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID: cmd.UserID, Amount: cmd.Amount, Reason: cmd.Reason,
		IdempotencyKey: cmd.IdempotencyKey, RequestID: cmd.RequestID,
	})
	if err != nil {
		return nil, mapBillingError(err)
	}
	return &tradeapp.WalletTransaction{ID: result.ID}, nil
}

func (a billingWalletAdapter) RefundConsumer(ctx context.Context, cmd tradeapp.WalletCommand) (*tradeapp.WalletTransaction, error) {
	result, err := a.wallet.RefundConsumer(ctx, billingapp.AdjustConsumerBalanceRequest{
		UserID:         cmd.UserID,
		Amount:         cmd.Amount,
		Reason:         cmd.Reason,
		IdempotencyKey: cmd.IdempotencyKey,
		RequestID:      cmd.RequestID,
	})
	if err != nil {
		return nil, mapBillingError(err)
	}
	return &tradeapp.WalletTransaction{ID: result.Transaction.ID}, nil
}

type allocationAdapter struct {
	alloc *allocapp.UseCase
}

func (a allocationAdapter) SelectRandomSuffix(ctx context.Context, cmd tradeapp.RandomSuffixSelectionCommand) (string, error) {
	suffix, err := a.alloc.SelectRandomInventorySuffix(ctx, allocapp.ProductSuffixSelectionRequest{
		ProjectID:            cmd.ProjectID,
		ProductID:            cmd.ProductID,
		BuyerUserID:          cmd.BuyerUserID,
		SupplyScopes:         allocationSupplyScopes(cmd.SupplyScopes),
		Selector:             cmd.Selector,
		FulfillExistingOrder: cmd.FulfillExistingOrder,
	})
	if err != nil {
		return "", mapAllocationError(err)
	}
	return suffix, nil
}

func (a allocationAdapter) HasAvailableInventory(ctx context.Context, cmd tradeapp.InventoryAvailabilityCommand) (bool, error) {
	return a.alloc.HasProductInventory(ctx, allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, EmailSuffix: cmd.EmailSuffix,
		PublicOnly: cmd.PublicOnly,
	})
}

func (a allocationAdapter) MarkInventoryUnavailable(ctx context.Context, cmd tradeapp.InventoryAvailabilityCommand) (bool, error) {
	return a.alloc.MarkProductInventoryUnavailable(ctx, allocapp.ProductInventoryAvailabilityRequest{
		ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, EmailSuffix: cmd.EmailSuffix,
		PublicOnly: cmd.PublicOnly,
	})
}

func (a allocationAdapter) Allocate(ctx context.Context, cmd tradeapp.AllocationCommand) (*tradeapp.AllocationResult, error) {
	scope := allocdomain.SupplyScopePublic
	if cmd.SupplyScope == tradeapp.SupplyScopeOwned {
		scope = allocdomain.SupplyScopeOwned
	}
	scopes := allocationSupplyScopes(cmd.SupplyScopes)
	result, err := a.alloc.Allocate(ctx, allocapp.AllocateCommand{
		OrderNo:              cmd.OrderNo,
		BuyerUserID:          cmd.BuyerUserID,
		ProjectProductID:     cmd.ProjectProductID,
		ServiceMode:          allocdomain.GmailServiceMode(cmd.ServiceMode),
		SupplyScope:          scope,
		SupplyScopes:         scopes,
		EmailSuffix:          cmd.EmailSuffix,
		RequiredUntil:        cmd.RequiredUntil,
		FulfillExistingOrder: cmd.FulfillExistingOrder,
	})
	if err != nil {
		return nil, mapAllocationError(err)
	}
	return &tradeapp.AllocationResult{
		OrderNo:     result.OrderNo,
		Type:        domain.AllocationType(result.Type),
		ID:          result.ID,
		ProductID:   result.ProductID,
		Created:     result.Created,
		Email:       result.Email,
		SupplyScope: tradeSupplyScope(result.SupplyScope),
		CreatedAt:   result.CreatedAt,
		ReleasedAt:  result.ReleasedAt,
	}, nil
}

func allocationSupplyScopes(scopes []tradeapp.SupplyScope) []allocdomain.SupplyScope {
	result := make([]allocdomain.SupplyScope, len(scopes))
	for i, item := range scopes {
		result[i] = allocdomain.SupplyScopePublic
		if item == tradeapp.SupplyScopeOwned {
			result[i] = allocdomain.SupplyScopeOwned
		}
	}
	return result
}

func (a allocationAdapter) FindAllocationsByOrders(ctx context.Context, orderNos []string) (map[string]tradeapp.AllocationResult, error) {
	allocations, err := a.alloc.FindAllocationsByOrders(ctx, orderNos)
	if err != nil {
		return nil, mapAllocationError(err)
	}
	result := make(map[string]tradeapp.AllocationResult, len(allocations))
	for orderNo, allocation := range allocations {
		result[orderNo] = tradeapp.AllocationResult{
			OrderNo: allocation.OrderNo, Type: domain.AllocationType(allocation.Type), ID: allocation.ID,
			ProductID: allocation.ProductID, Created: allocation.Created,
			Email: allocation.Email, SupplyScope: tradeSupplyScope(allocation.SupplyScope),
			CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt,
		}
	}
	return result, nil
}

func (a allocationAdapter) ImportHistoricalMicrosoftAllocation(ctx context.Context, cmd tradeapp.HistoricalMicrosoftAllocationCommand) (*tradeapp.AllocationResult, error) {
	result, err := a.alloc.ImportHistoricalMicrosoftAllocation(ctx, allocapp.HistoricalMicrosoftAllocationCommand{
		AliasOwnerID: cmd.AliasOwnerID, ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
		Mailbox: allocdomain.MicrosoftMailbox(cmd.Mailbox), Email: cmd.Email,
		CreatedAt: cmd.CreatedAt, ReleasedAt: cmd.ReleasedAt,
	})
	if err != nil {
		if errors.Is(err, allocdomain.ErrHistoricalAllocationOwnerRequired) {
			return nil, tradeapp.ErrHistoricalAllocationOwnerRequired
		}
		return nil, mapAllocationError(err)
	}
	if result == nil {
		return nil, nil
	}
	return &tradeapp.AllocationResult{
		OrderNo: result.OrderNo, Type: domain.AllocationType(result.Type), ID: result.ID,
		ProductID: result.ProductID, Created: result.Created, Email: result.Email,
		SupplyScope: tradeSupplyScope(result.SupplyScope), CreatedAt: result.CreatedAt, ReleasedAt: result.ReleasedAt,
	}, nil
}

func (a allocationAdapter) ImportHistoricalGmailAllocation(ctx context.Context, cmd tradeapp.HistoricalGmailAllocationCommand) (*tradeapp.AllocationResult, error) {
	result, err := a.alloc.ImportHistoricalGmailAllocation(ctx, allocapp.HistoricalGmailAllocationCommand{
		ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
		Mailbox: allocdomain.GmailMailbox(cmd.Mailbox), Email: cmd.Email,
		CreatedAt: cmd.CreatedAt, ReleasedAt: cmd.ReleasedAt,
	})
	if err != nil {
		return nil, mapAllocationError(err)
	}
	if result == nil {
		return nil, nil
	}
	return &tradeapp.AllocationResult{
		OrderNo: result.OrderNo, Type: domain.AllocationType(result.Type), ID: result.ID,
		ProductID: result.ProductID, Created: result.Created, Email: result.Email,
		SupplyScope: tradeSupplyScope(result.SupplyScope), CreatedAt: result.CreatedAt, ReleasedAt: result.ReleasedAt,
	}, nil
}

func tradeSupplyScope(scope allocdomain.SupplyScope) tradeapp.SupplyScope {
	if scope == allocdomain.SupplyScopeOwned {
		return tradeapp.SupplyScopeOwned
	}
	return tradeapp.SupplyScopePublic
}

func (a allocationAdapter) ReleaseByOrder(ctx context.Context, orderNo string) error {
	_, err := a.alloc.ReleaseByOrder(ctx, orderNo)
	if err != nil && !errors.Is(err, allocdomain.ErrAllocationNotFound) {
		return mapAllocationError(err)
	}
	return nil
}

type orderTokenAdapter struct {
	tokens *openapiapp.UseCase
}

func (a orderTokenAdapter) IssueOrderToken(ctx context.Context, orderNo string, expireAt *time.Time) (*tradeapp.OrderToken, error) {
	token, err := a.tokens.IssueOrderToken(ctx, orderNo, expireAt)
	if err != nil {
		return nil, err
	}
	return &tradeapp.OrderToken{TokenPlain: token.TokenPlain, ExpireAt: token.ExpireAt}, nil
}

func (a orderTokenAdapter) FindOrderTokenByOrder(ctx context.Context, orderNo string) (*tradeapp.OrderToken, error) {
	token, err := a.tokens.FindOrderTokenByOrder(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	return &tradeapp.OrderToken{TokenPlain: token.TokenPlain, ExpireAt: token.ExpireAt}, nil
}

func (a orderTokenAdapter) ExtendOrderToken(ctx context.Context, orderNo string, expireAt time.Time) error {
	return a.tokens.ExtendOrderToken(ctx, orderNo, expireAt)
}

func (a orderTokenAdapter) DisableOrderToken(ctx context.Context, orderNo string, reason string) error {
	return a.tokens.DisableOrderToken(ctx, orderNo, reason)
}

func mapCoreError(err error) error {
	switch {
	case errors.Is(err, coredomain.ErrForbiddenProject), errors.Is(err, coredomain.ErrProjectNotFound):
		return domain.ErrProjectUnavailable
	case errors.Is(err, coredomain.ErrInvalidProduct), errors.Is(err, coredomain.ErrInvalidProjectStatus), errors.Is(err, coredomain.ErrInvalidProject):
		return domain.ErrProjectUnavailable
	default:
		return err
	}
}

func mapBillingError(err error) error {
	switch {
	case errors.Is(err, billingdomain.ErrInsufficientBalance):
		return domain.ErrInsufficientBalance
	case errors.Is(err, billingdomain.ErrIdempotencyRequired):
		return domain.ErrIdempotencyRequired
	case errors.Is(err, billingdomain.ErrIdempotencyConflict):
		return domain.ErrIdempotencyConflict
	default:
		return err
	}
}

func mapAllocationError(err error) error {
	switch {
	case errors.Is(err, allocdomain.ErrDefinitiveInventoryExhausted):
		return domain.ErrDefinitiveInventoryExhausted
	case errors.Is(err, allocdomain.ErrInsufficientInventory):
		return domain.ErrInsufficientInventory
	case errors.Is(err, allocdomain.ErrInvalidAllocationRequest):
		return domain.ErrInvalidOrderRequest
	case errors.Is(err, allocdomain.ErrProjectNotAllocatable):
		return domain.ErrProjectUnavailable
	default:
		return err
	}
}
