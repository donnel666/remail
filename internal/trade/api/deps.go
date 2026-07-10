package api

import (
	"context"
	"errors"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	billingdomain "github.com/donnel666/remail/internal/billing/domain"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	openapiapp "github.com/donnel666/remail/internal/openapi/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/donnel666/remail/internal/trade/infra"
	"gorm.io/gorm"
)

type Module struct {
	UseCase       *tradeapp.UseCase
	OperationLogs governanceapp.OperationLogPort
}

func NewModule(db *gorm.DB, coreProjects *coreapp.ProjectUseCase, billingWallet *billingapp.WalletUseCase, alloc *allocapp.UseCase, tokens *openapiapp.UseCase) *Module {
	repo := infra.NewRepo(db)
	systemLogs := governanceinfra.NewSystemLogRepo(db)
	operationLogs := governanceinfra.NewOperationLogRepo(db)
	uc := tradeapp.NewUseCase(
		repo,
		coreOrderingAdapter{projects: coreProjects},
		billingWalletAdapter{wallet: billingWallet},
		allocationAdapter{alloc: alloc},
		orderTokenAdapter{tokens: tokens},
	)
	uc.SetOrderDeliveryPort(orderDeliveryAdapter{db: db})
	uc.SetProjectNamePort(projectNameAdapter{db: db})
	uc.SetSystemLogPort(systemLogs)
	return &Module{
		UseCase:       uc,
		OperationLogs: operationLogs,
	}
}

// projectNameAdapter is a read-model helper that resolves project display
// names for order listings, mirroring orderDeliveryAdapter's approach.
type projectNameAdapter struct {
	db *gorm.DB
}

func (a projectNameAdapter) ProjectNames(ctx context.Context, projectIDs []uint) (map[uint]string, error) {
	result := make(map[uint]string, len(projectIDs))
	if len(projectIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := a.db.WithContext(ctx).
		Table("projects").
		Select("id, name").
		Where("id IN ?", projectIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = row.Name
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
	if err := a.db.WithContext(ctx).
		Table("mailmatch_order_delivery_heads AS h").
		Select("h.order_id, COALESCE(m.verification_code, '') AS verification_code, h.message_received_at AS received_at").
		Joins("LEFT JOIN mailmatch_messages AS m ON m.id = h.message_id").
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
}

func (a coreOrderingAdapter) GetOrderingQuote(ctx context.Context, projectID uint, productID uint, buyerUserID uint, serviceMode domain.ServiceMode) (*tradeapp.OrderingQuote, error) {
	quote, err := a.projects.GetOrderingQuote(ctx, projectID, productID, buyerUserID, string(serviceMode))
	if err != nil {
		return nil, mapCoreError(err)
	}
	return &tradeapp.OrderingQuote{
		ProjectID:               quote.ProjectID,
		ProductID:               quote.ProductID,
		ProductType:             domain.ProductType(quote.ProductType),
		PayAmount:               quote.PayAmount,
		CodeWindowMinutes:       quote.CodeWindowMinutes,
		ActivationWindowMinutes: quote.ActivationWindowMinutes,
		WarrantyMinutes:         quote.WarrantyMinutes,
	}, nil
}

type billingWalletAdapter struct {
	wallet *billingapp.WalletUseCase
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

func (a allocationAdapter) Allocate(ctx context.Context, cmd tradeapp.AllocationCommand) (*tradeapp.AllocationResult, error) {
	scope := allocdomain.SupplyScopePublic
	if cmd.SupplyScope == tradeapp.SupplyScopeOwned {
		scope = allocdomain.SupplyScopeOwned
	}
	result, err := a.alloc.Allocate(ctx, allocapp.AllocateCommand{
		OrderNo:          cmd.OrderNo,
		BuyerUserID:      cmd.BuyerUserID,
		ProjectProductID: cmd.ProjectProductID,
		SupplyScope:      scope,
		EmailSuffix:      cmd.EmailSuffix,
	})
	if err != nil {
		return nil, mapAllocationError(err)
	}
	return &tradeapp.AllocationResult{
		Type:        domain.AllocationType(result.Type),
		ID:          result.ID,
		Email:       result.Email,
		SupplyScope: tradeSupplyScope(result.SupplyScope),
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
	case errors.Is(err, allocdomain.ErrInsufficientInventory):
		return domain.ErrInsufficientInventory
	case errors.Is(err, allocdomain.ErrProjectNotAllocatable), errors.Is(err, allocdomain.ErrInvalidAllocationRequest):
		return domain.ErrProjectUnavailable
	default:
		return err
	}
}
