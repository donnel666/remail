package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OrderModel struct {
	ID                   uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo              string     `gorm:"type:varchar(64);not null;column:order_no"`
	UserID               uint       `gorm:"not null;column:user_id"`
	ProjectID            uint       `gorm:"not null;column:project_id"`
	ProjectProductID     uint       `gorm:"not null;column:project_product_id"`
	ProductType          string     `gorm:"type:varchar(32);not null;column:product_type"`
	ServiceMode          string     `gorm:"type:varchar(32);not null;column:service_mode"`
	SupplyPolicy         string     `gorm:"type:varchar(32);not null;column:supply_policy"`
	Status               string     `gorm:"type:varchar(32);not null"`
	PayAmount            string     `gorm:"type:decimal(18,2);not null;column:pay_amount"`
	RefundAmount         string     `gorm:"type:decimal(18,2);not null;column:refund_amount"`
	DebitTxID            *uint      `gorm:"column:debit_tx_id"`
	RefundTxID           *uint      `gorm:"column:refund_tx_id"`
	AllocationType       *string    `gorm:"type:varchar(32);column:allocation_type"`
	MicrosoftAllocID     *uint      `gorm:"column:microsoft_alloc_id"`
	DomainAllocID        *uint      `gorm:"column:domain_alloc_id"`
	DeliveryEmail        string     `gorm:"type:varchar(255);not null;column:delivery_email"`
	ReceiveStartedAt     *time.Time `gorm:"column:receive_started_at"`
	ReceiveUntil         *time.Time `gorm:"column:receive_until"`
	ActivatedAt          *time.Time `gorm:"column:activated_at"`
	AfterSaleUntil       *time.Time `gorm:"column:after_sale_until"`
	ClientChannel        string     `gorm:"type:varchar(32);not null;column:client_channel"`
	APIKeyID             *uint      `gorm:"column:api_key_id"`
	IdempotencyKey       string     `gorm:"type:varchar(128);not null;column:idempotency_key"`
	RequestFingerprint   string     `gorm:"type:char(64);not null;column:request_fingerprint"`
	ServiceCleanupStatus string     `gorm:"type:varchar(32);not null;column:service_cleanup_status"`
	ArchivedAt           *time.Time `gorm:"column:archived_at"`
	CreatedAt            time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt            time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
	Version              int        `gorm:"not null;default:1"`
}

func (OrderModel) TableName() string { return "orders" }

type OrderEventModel struct {
	ID           uint           `gorm:"primaryKey;autoIncrement"`
	EventNo      string         `gorm:"type:varchar(64);not null;column:event_no"`
	OrderNo      string         `gorm:"type:varchar(64);not null;column:order_no"`
	EventType    string         `gorm:"type:varchar(64);not null;column:event_type"`
	FromStatus   sql.NullString `gorm:"type:varchar(32);column:from_status"`
	ToStatus     sql.NullString `gorm:"type:varchar(32);column:to_status"`
	OperatorType string         `gorm:"type:varchar(32);not null;column:operator_type"`
	Reason       string         `gorm:"type:varchar(500);not null;default:''"`
	EventContext sql.NullString `gorm:"type:json;column:event_context"`
	CreatedAt    time.Time      `gorm:"not null;autoCreateTime;column:created_at"`
}

func (OrderEventModel) TableName() string { return "order_events" }

type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx)
	}
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return fn(platform.WithGormTx(ctx, tx))
		})
		if err == nil || !isDeadlockError(err) {
			return err
		}
		time.Sleep(deadlockBackoff(attempt))
	}
	return err
}

func (r *Repo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *Repo) LoadOrCreatePendingOrder(ctx context.Context, cmd tradeapp.CreatePendingOrderCommand) (*domain.Order, bool, error) {
	var model OrderModel
	created := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		candidate := OrderModel{
			OrderNo:              cmd.OrderNo,
			UserID:               cmd.UserID,
			ProjectID:            cmd.ProjectID,
			ProjectProductID:     cmd.ProjectProductID,
			ProductType:          string(cmd.ProductType),
			ServiceMode:          string(cmd.ServiceMode),
			SupplyPolicy:         string(cmd.SupplyPolicy),
			Status:               string(domain.OrderStatusPendingPayment),
			PayAmount:            cmd.PayAmount,
			RefundAmount:         "0.00",
			ClientChannel:        string(cmd.ClientChannel),
			APIKeyID:             cmd.APIKeyID,
			IdempotencyKey:       strings.TrimSpace(cmd.IdempotencyKey),
			RequestFingerprint:   cmd.RequestFingerprint,
			ServiceCleanupStatus: "none",
		}
		if err := tx.Create(&candidate).Error; err != nil {
			if !isDuplicateKeyError(err) {
				return fmt.Errorf("create order: %w", err)
			}
			return r.lockOrderByIdempotency(txCtx, tx, cmd.ClientChannel, cmd.UserID, cmd.APIKeyID, cmd.IdempotencyKey, cmd.RequestFingerprint, &model)
		}
		model = candidate
		created = true
		return r.appendEvent(txCtx, tx, candidate.OrderNo, "order.created", nil, ptrStatus(domain.OrderStatusPendingPayment), operatorForChannel(cmd.ClientChannel), "")
	})
	if err != nil {
		return nil, false, err
	}
	order := orderModelToDomain(model)
	return &order, created, nil
}

func (r *Repo) FindOrder(ctx context.Context, orderNo string) (*domain.Order, error) {
	var model OrderModel
	if err := r.dbFor(ctx).First(&model, "order_no = ?", strings.TrimSpace(orderNo)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrOrderNotFound
		}
		return nil, fmt.Errorf("find order: %w", err)
	}
	order := orderModelToDomain(model)
	return &order, nil
}

func (r *Repo) LockOrderForUpdate(ctx context.Context, orderNo string) (*domain.Order, error) {
	var model OrderModel
	if err := lockOrder(ctx, r.dbFor(ctx), strings.TrimSpace(orderNo), &model); err != nil {
		return nil, err
	}
	order := orderModelToDomain(model)
	return &order, nil
}

func (r *Repo) MarkPaid(ctx context.Context, cmd tradeapp.MarkPaidCommand) (*domain.Order, error) {
	orderNo := strings.TrimSpace(cmd.OrderNo)
	payAmount := strings.TrimSpace(cmd.PayAmount)
	if orderNo == "" || cmd.DebitTxID == 0 || payAmount == "" {
		return nil, domain.ErrInvalidOrderRequest
	}
	var model OrderModel
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		if domain.OrderStatus(model.Status) != domain.OrderStatusPendingPayment {
			return domain.ErrOrderStateConflict
		}
		previous := domain.OrderStatus(model.Status)
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND status = ?", orderNo, string(domain.OrderStatusPendingPayment)).
			Updates(map[string]any{
				"status":      string(domain.OrderStatusPaid),
				"pay_amount":  payAmount,
				"debit_tx_id": cmd.DebitTxID,
				"version":     gorm.Expr("version + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("mark order paid: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload paid order: %w", err)
		}
		return r.appendEvent(txCtx, tx, orderNo, "order.paid", &previous, ptrStatus(domain.OrderStatusPaid), operatorForChannel(domain.ClientChannel(model.ClientChannel)), "")
	})
	if err != nil {
		return nil, err
	}
	order := orderModelToDomain(model)
	return &order, nil
}

func (r *Repo) MarkActive(ctx context.Context, cmd tradeapp.MarkActiveCommand) (*domain.Order, error) {
	var model OrderModel
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, cmd.OrderNo, &model); err != nil {
			return err
		}
		if domain.OrderStatus(model.Status) != domain.OrderStatusPaid {
			return domain.ErrOrderStateConflict
		}
		previous := domain.OrderStatus(model.Status)
		allocationType := string(cmd.AllocationType)
		updates := map[string]any{
			"status":                 string(domain.OrderStatusActive),
			"allocation_type":        allocationType,
			"delivery_email":         strings.ToLower(strings.TrimSpace(cmd.DeliveryEmail)),
			"receive_started_at":     cmd.ReceiveStartedAt,
			"receive_until":          cmd.ReceiveUntil,
			"after_sale_until":       cmd.AfterSaleUntil,
			"version":                gorm.Expr("version + 1"),
			"microsoft_alloc_id":     nil,
			"domain_alloc_id":        nil,
			"service_cleanup_status": "none",
		}
		switch cmd.AllocationType {
		case domain.AllocationTypeMicrosoft:
			updates["microsoft_alloc_id"] = cmd.AllocationID
		case domain.AllocationTypeDomain:
			updates["domain_alloc_id"] = cmd.AllocationID
		default:
			return domain.ErrInvalidOrderRequest
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND status = ?", cmd.OrderNo, string(domain.OrderStatusPaid)).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("mark order active: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", cmd.OrderNo).Error; err != nil {
			return fmt.Errorf("reload active order: %w", err)
		}
		return r.appendEvent(txCtx, tx, cmd.OrderNo, "order.active", &previous, ptrStatus(domain.OrderStatusActive), operatorForChannel(domain.ClientChannel(model.ClientChannel)), "")
	})
	if err != nil {
		return nil, err
	}
	order := orderModelToDomain(model)
	return &order, nil
}

func (r *Repo) MarkFailed(ctx context.Context, cmd tradeapp.MarkFailedCommand) (*domain.Order, error) {
	var model OrderModel
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, cmd.OrderNo, &model); err != nil {
			return err
		}
		current := domain.OrderStatus(model.Status)
		if current == domain.OrderStatusFailed {
			return nil
		}
		if current != domain.OrderStatusPendingPayment && current != domain.OrderStatusPaid {
			return domain.ErrOrderStateConflict
		}
		updates := map[string]any{
			"status":  string(domain.OrderStatusFailed),
			"version": gorm.Expr("version + 1"),
		}
		if cmd.RefundTxID != nil {
			updates["refund_tx_id"] = *cmd.RefundTxID
			updates["refund_amount"] = cmd.RefundAmount
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND status IN ?", cmd.OrderNo, []string{string(domain.OrderStatusPendingPayment), string(domain.OrderStatusPaid)}).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("mark order failed: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", cmd.OrderNo).Error; err != nil {
			return fmt.Errorf("reload failed order: %w", err)
		}
		return r.appendEvent(txCtx, tx, cmd.OrderNo, "order.failed", &current, ptrStatus(domain.OrderStatusFailed), domain.OperatorTypeSystem, cmd.Reason)
	})
	if err != nil {
		return nil, err
	}
	order := orderModelToDomain(model)
	return &order, nil
}

func (r *Repo) RefundOrder(ctx context.Context, cmd tradeapp.RefundOrderCommand) (*domain.Order, bool, error) {
	orderNo := strings.TrimSpace(cmd.OrderNo)
	refundAmount := strings.TrimSpace(cmd.RefundAmount)
	if orderNo == "" || cmd.RefundTxID == 0 || refundAmount == "" {
		return nil, false, domain.ErrInvalidOrderRequest
	}
	operator := cmd.Operator
	if operator == "" {
		operator = domain.OperatorTypeSystem
	}
	var model OrderModel
	changed := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		current := domain.OrderStatus(model.Status)
		if current == domain.OrderStatusRefunded {
			return nil
		}
		if current != domain.OrderStatusActive && current != domain.OrderStatusCompleted {
			return domain.ErrOrderStateConflict
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND status IN ?", orderNo, []string{string(domain.OrderStatusActive), string(domain.OrderStatusCompleted)}).
			Updates(map[string]any{
				"status":        string(domain.OrderStatusRefunded),
				"refund_tx_id":  cmd.RefundTxID,
				"refund_amount": refundAmount,
				"version":       gorm.Expr("version + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("refund order: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload refunded order: %w", err)
		}
		changed = true
		return r.appendEvent(txCtx, tx, orderNo, "order.refunded", &current, ptrStatus(domain.OrderStatusRefunded), operator, cmd.Reason)
	})
	if err != nil {
		return nil, false, err
	}
	order := orderModelToDomain(model)
	return &order, changed, nil
}

func (r *Repo) AttachFailedOrderRefund(ctx context.Context, cmd tradeapp.RefundOrderCommand) (*domain.Order, bool, error) {
	orderNo := strings.TrimSpace(cmd.OrderNo)
	refundAmount := strings.TrimSpace(cmd.RefundAmount)
	if orderNo == "" || cmd.RefundTxID == 0 || refundAmount == "" {
		return nil, false, domain.ErrInvalidOrderRequest
	}
	operator := cmd.Operator
	if operator == "" {
		operator = domain.OperatorTypeSystem
	}
	var model OrderModel
	changed := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		current := domain.OrderStatus(model.Status)
		if current != domain.OrderStatusFailed || model.DebitTxID == nil || model.RefundTxID != nil {
			return domain.ErrOrderStateConflict
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND status = ? AND debit_tx_id IS NOT NULL AND refund_tx_id IS NULL", orderNo, string(domain.OrderStatusFailed)).
			Updates(map[string]any{
				"refund_tx_id":  cmd.RefundTxID,
				"refund_amount": refundAmount,
				"version":       gorm.Expr("version + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("attach failed order refund: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload failed refunded order: %w", err)
		}
		changed = true
		return r.appendEvent(txCtx, tx, orderNo, "order.refund_retried", &current, ptrStatus(domain.OrderStatusFailed), operator, cmd.Reason)
	})
	if err != nil {
		return nil, false, err
	}
	order := orderModelToDomain(model)
	return &order, changed, nil
}

func (r *Repo) CompleteExpiredOrder(ctx context.Context, orderNo string, reason string) (*domain.Order, bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	var model OrderModel
	changed := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		current := domain.OrderStatus(model.Status)
		if current == domain.OrderStatusCompleted {
			return nil
		}
		if current != domain.OrderStatusActive {
			return nil
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND status = ?", orderNo, string(domain.OrderStatusActive)).
			Updates(map[string]any{
				"status":  string(domain.OrderStatusCompleted),
				"version": gorm.Expr("version + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("complete expired order: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload expired completed order: %w", err)
		}
		changed = true
		return r.appendEvent(txCtx, tx, orderNo, "order.completed", &current, ptrStatus(domain.OrderStatusCompleted), domain.OperatorTypeSystem, reason)
	})
	if err != nil {
		return nil, false, err
	}
	order := orderModelToDomain(model)
	return &order, changed, nil
}

func (r *Repo) CloseActiveOrder(ctx context.Context, orderNo string, reason string) (*domain.Order, bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	var model OrderModel
	changed := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		current := domain.OrderStatus(model.Status)
		if current == domain.OrderStatusClosed {
			return nil
		}
		if current != domain.OrderStatusActive {
			return domain.ErrOrderStateConflict
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND status = ?", orderNo, string(domain.OrderStatusActive)).
			Updates(map[string]any{
				"status":  string(domain.OrderStatusClosed),
				"version": gorm.Expr("version + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("close active order: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload closed order: %w", err)
		}
		changed = true
		return r.appendEvent(txCtx, tx, orderNo, "order.closed", &current, ptrStatus(domain.OrderStatusClosed), domain.OperatorTypeAdmin, reason)
	})
	if err != nil {
		return nil, false, err
	}
	order := orderModelToDomain(model)
	return &order, changed, nil
}

func (r *Repo) MarkServiceCleanup(ctx context.Context, orderNo string, status string) error {
	orderNo = strings.TrimSpace(orderNo)
	status = strings.TrimSpace(status)
	if orderNo == "" || (status != "succeeded" && status != "partial_failure") {
		return domain.ErrInvalidOrderRequest
	}
	result := r.dbFor(ctx).Model(&OrderModel{}).
		Where("order_no = ?", orderNo).
		Update("service_cleanup_status", status)
	if result.Error != nil {
		return fmt.Errorf("mark order service cleanup: %w", result.Error)
	}
	return nil
}

func (r *Repo) Archive(ctx context.Context, orderNo string, userID uint, archivedAt time.Time) (*domain.Order, error) {
	var model OrderModel
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		if model.UserID != userID {
			return domain.ErrOrderForbidden
		}
		status := domain.OrderStatus(model.Status)
		if !domain.IsTerminalStatus(status) {
			return domain.ErrOrderStateConflict
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND user_id = ?", orderNo, userID).
			Updates(map[string]any{"archived_at": archivedAt, "version": gorm.Expr("version + 1")})
		if result.Error != nil {
			return fmt.Errorf("archive order: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload archived order: %w", err)
		}
		return r.appendEvent(txCtx, tx, orderNo, "order.archived", &status, &status, domain.OperatorTypeUser, "")
	})
	if err != nil {
		return nil, err
	}
	order := orderModelToDomain(model)
	return &order, nil
}

func (r *Repo) ListOrders(ctx context.Context, filter tradeapp.OrderListFilter, offset, limit int) ([]domain.Order, int64, error) {
	query := r.dbFor(ctx).Model(&OrderModel{})
	query = applyOrderFilter(query, filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count orders: %w", err)
	}
	var models []OrderModel
	if err := applyOrderFilter(r.dbFor(ctx).Model(&OrderModel{}), filter).
		Order("created_at DESC, id DESC").
		Offset(offset).
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list orders: %w", err)
	}
	items := make([]domain.Order, len(models))
	for i := range models {
		items[i] = orderModelToDomain(models[i])
	}
	return items, total, nil
}

func (r *Repo) ListEvents(ctx context.Context, orderNo string, userID uint, isAdmin bool, offset, limit int) ([]domain.OrderEvent, int64, error) {
	var order OrderModel
	if err := r.dbFor(ctx).Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, domain.ErrOrderNotFound
		}
		return nil, 0, fmt.Errorf("find order for events: %w", err)
	}
	if !isAdmin && order.UserID != userID {
		return nil, 0, domain.ErrOrderForbidden
	}
	query := r.dbFor(ctx).Model(&OrderEventModel{}).Where("order_no = ?", orderNo)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count order events: %w", err)
	}
	var models []OrderEventModel
	if err := r.dbFor(ctx).Where("order_no = ?", orderNo).
		Order("created_at ASC, id ASC").
		Offset(offset).
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("list order events: %w", err)
	}
	items := make([]domain.OrderEvent, len(models))
	for i := range models {
		items[i] = eventModelToDomain(models[i])
	}
	return items, total, nil
}

func (r *Repo) ListExpiredCodeOrderNos(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return r.listOrderNos(ctx, limit, "status = ? AND service_mode = ? AND receive_until IS NOT NULL AND receive_until < ?",
		string(domain.OrderStatusActive),
		string(domain.ServiceModeCode),
		now.UTC(),
	)
}

func (r *Repo) ListExpiredPurchaseActivationOrderNos(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return r.listOrderNos(ctx, limit, "status = ? AND service_mode = ? AND activated_at IS NULL AND receive_until IS NOT NULL AND receive_until < ?",
		string(domain.OrderStatusActive),
		string(domain.ServiceModePurchase),
		now.UTC(),
	)
}

func (r *Repo) ListExpiredPurchaseWarrantyOrderNos(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return r.listOrderNos(ctx, limit, "status = ? AND service_mode = ? AND activated_at IS NOT NULL AND after_sale_until IS NOT NULL AND after_sale_until < ?",
		string(domain.OrderStatusActive),
		string(domain.ServiceModePurchase),
		now.UTC(),
	)
}

func (r *Repo) ListCodeOrderNosReadyForCleanup(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return r.listOrderNos(ctx, limit, "status IN ? AND service_mode = ? AND service_cleanup_status = ? AND after_sale_until IS NOT NULL AND after_sale_until < ?",
		[]string{string(domain.OrderStatusCompleted), string(domain.OrderStatusRefunded)},
		string(domain.ServiceModeCode),
		"none",
		now.UTC(),
	)
}

func (r *Repo) listOrderNos(ctx context.Context, limit int, condition string, args ...any) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	var orderNos []string
	if err := r.dbFor(ctx).Model(&OrderModel{}).
		Where(condition, args...).
		Order("id ASC").
		Limit(limit).
		Pluck("order_no", &orderNos).Error; err != nil {
		return nil, fmt.Errorf("list lifecycle order numbers: %w", err)
	}
	return orderNos, nil
}

func (r *Repo) CompleteCodeOrder(ctx context.Context, orderNo string, matchedAt time.Time, readUntil time.Time) (*domain.Order, bool, error) {
	var model OrderModel
	changed := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		if domain.ServiceMode(model.ServiceMode) != domain.ServiceModeCode {
			return nil
		}
		current := domain.OrderStatus(model.Status)
		if current == domain.OrderStatusCompleted {
			return nil
		}
		if current != domain.OrderStatusActive {
			return nil
		}
		previous := current
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND service_mode = ? AND status = ?", orderNo, string(domain.ServiceModeCode), string(domain.OrderStatusActive)).
			Updates(map[string]any{
				"status":           string(domain.OrderStatusCompleted),
				"receive_until":    readUntil.UTC(),
				"after_sale_until": readUntil.UTC(),
				"version":          gorm.Expr("version + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("complete code order: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload completed code order: %w", err)
		}
		changed = true
		reason := "Code matched at " + matchedAt.UTC().Format(time.RFC3339)
		return r.appendEvent(txCtx, tx, orderNo, "order.completed", &previous, ptrStatus(domain.OrderStatusCompleted), domain.OperatorTypeSystem, reason)
	})
	if err != nil {
		return nil, false, err
	}
	order := orderModelToDomain(model)
	return &order, changed, nil
}

func (r *Repo) ActivatePurchaseOrder(ctx context.Context, orderNo string, matchedAt time.Time, afterSaleUntil time.Time) (*domain.Order, bool, error) {
	var model OrderModel
	changed := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		if err := lockOrder(txCtx, tx, orderNo, &model); err != nil {
			return err
		}
		if domain.ServiceMode(model.ServiceMode) != domain.ServiceModePurchase {
			return nil
		}
		current := domain.OrderStatus(model.Status)
		if current != domain.OrderStatusActive {
			return nil
		}
		if model.ActivatedAt != nil && model.AfterSaleUntil != nil && !afterSaleUntil.After(*model.AfterSaleUntil) {
			return nil
		}
		previous := current
		updates := map[string]any{
			"activated_at":     matchedAt.UTC(),
			"after_sale_until": afterSaleUntil.UTC(),
			"version":          gorm.Expr("version + 1"),
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND service_mode = ? AND status = ?", orderNo, string(domain.ServiceModePurchase), string(domain.OrderStatusActive)).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("activate purchase order: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return domain.ErrOrderStateConflict
		}
		if err := tx.First(&model, "order_no = ?", orderNo).Error; err != nil {
			return fmt.Errorf("reload activated purchase order: %w", err)
		}
		changed = true
		reason := "Purchase activated by matched code at " + matchedAt.UTC().Format(time.RFC3339)
		return r.appendEvent(txCtx, tx, orderNo, "order.purchase_activated", &previous, ptrStatus(domain.OrderStatusActive), domain.OperatorTypeSystem, reason)
	})
	if err != nil {
		return nil, false, err
	}
	order := orderModelToDomain(model)
	return &order, changed, nil
}

func (r *Repo) lockOrderByIdempotency(ctx context.Context, tx *gorm.DB, channel domain.ClientChannel, userID uint, apiKeyID *uint, idempotencyKey string, fingerprint string, out *OrderModel) error {
	subject := idempotencySubject(channel, userID, apiKeyID)
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("idempotency_subject = ? AND idempotency_key = ?", subject, strings.TrimSpace(idempotencyKey)).
		First(out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrIdempotencyConflict
		}
		return fmt.Errorf("find idempotent order: %w", err)
	}
	if out.RequestFingerprint != fingerprint {
		return domain.ErrIdempotencyConflict
	}
	return nil
}

func lockOrder(ctx context.Context, tx *gorm.DB, orderNo string, out *OrderModel) error {
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(out, "order_no = ?", strings.TrimSpace(orderNo)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrOrderNotFound
		}
		return fmt.Errorf("lock order: %w", err)
	}
	return nil
}

func (r *Repo) appendEvent(ctx context.Context, tx *gorm.DB, orderNo string, eventType string, fromStatus *domain.OrderStatus, toStatus *domain.OrderStatus, operator domain.OperatorType, reason string) error {
	model := OrderEventModel{
		EventNo:      nextEventNo(),
		OrderNo:      strings.TrimSpace(orderNo),
		EventType:    strings.TrimSpace(eventType),
		OperatorType: string(operator),
		Reason:       strings.TrimSpace(reason),
	}
	if fromStatus != nil {
		model.FromStatus = sql.NullString{String: string(*fromStatus), Valid: true}
	}
	if toStatus != nil {
		model.ToStatus = sql.NullString{String: string(*toStatus), Valid: true}
	}
	if err := tx.WithContext(ctx).Create(&model).Error; err != nil {
		return fmt.Errorf("append order event: %w", err)
	}
	return nil
}

func applyOrderFilter(query *gorm.DB, filter tradeapp.OrderListFilter) *gorm.DB {
	if !filter.IsAdmin || filter.Scope != "all" {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", string(filter.Status))
	}
	if filter.ServiceMode != "" {
		query = query.Where("service_mode = ?", string(filter.ServiceMode))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := search + "%"
		query = query.Where("order_no LIKE ? OR delivery_email LIKE ?", like, like)
	}
	return query
}

func orderModelToDomain(model OrderModel) domain.Order {
	var allocationType *domain.AllocationType
	if model.AllocationType != nil {
		value := domain.AllocationType(*model.AllocationType)
		allocationType = &value
	}
	return domain.Order{
		ID:                   model.ID,
		OrderNo:              model.OrderNo,
		UserID:               model.UserID,
		ProjectID:            model.ProjectID,
		ProjectProductID:     model.ProjectProductID,
		ProductType:          domain.ProductType(model.ProductType),
		ServiceMode:          domain.ServiceMode(model.ServiceMode),
		SupplyPolicy:         domain.SupplyPolicy(model.SupplyPolicy),
		Status:               domain.OrderStatus(model.Status),
		PayAmount:            model.PayAmount,
		RefundAmount:         model.RefundAmount,
		DebitTxID:            model.DebitTxID,
		RefundTxID:           model.RefundTxID,
		AllocationType:       allocationType,
		MicrosoftAllocID:     model.MicrosoftAllocID,
		DomainAllocID:        model.DomainAllocID,
		DeliveryEmail:        model.DeliveryEmail,
		ReceiveStartedAt:     model.ReceiveStartedAt,
		ReceiveUntil:         model.ReceiveUntil,
		ActivatedAt:          model.ActivatedAt,
		AfterSaleUntil:       model.AfterSaleUntil,
		ClientChannel:        domain.ClientChannel(model.ClientChannel),
		APIKeyID:             model.APIKeyID,
		IdempotencyKey:       model.IdempotencyKey,
		RequestFingerprint:   model.RequestFingerprint,
		ServiceCleanupStatus: model.ServiceCleanupStatus,
		ArchivedAt:           model.ArchivedAt,
		CreatedAt:            model.CreatedAt,
		UpdatedAt:            model.UpdatedAt,
		Version:              model.Version,
	}
}

func eventModelToDomain(model OrderEventModel) domain.OrderEvent {
	var fromStatus *domain.OrderStatus
	if model.FromStatus.Valid {
		value := domain.OrderStatus(model.FromStatus.String)
		fromStatus = &value
	}
	var toStatus *domain.OrderStatus
	if model.ToStatus.Valid {
		value := domain.OrderStatus(model.ToStatus.String)
		toStatus = &value
	}
	contextValue := ""
	if model.EventContext.Valid {
		contextValue = model.EventContext.String
	}
	return domain.OrderEvent{
		ID:           model.ID,
		EventNo:      model.EventNo,
		OrderNo:      model.OrderNo,
		EventType:    model.EventType,
		FromStatus:   fromStatus,
		ToStatus:     toStatus,
		OperatorType: domain.OperatorType(model.OperatorType),
		Reason:       model.Reason,
		EventContext: contextValue,
		CreatedAt:    model.CreatedAt,
	}
}

func idempotencySubject(channel domain.ClientChannel, userID uint, apiKeyID *uint) string {
	if channel == domain.ClientChannelAPIKey && apiKeyID != nil {
		return fmt.Sprintf("api_key:%d", *apiKeyID)
	}
	return fmt.Sprintf("user:%d", userID)
}

func operatorForChannel(channel domain.ClientChannel) domain.OperatorType {
	if channel == domain.ClientChannelAPIKey {
		return domain.OperatorTypeOpenAPI
	}
	return domain.OperatorTypeUser
}

func ptrStatus(value domain.OrderStatus) *domain.OrderStatus {
	return &value
}

func nextEventNo() string {
	return "OE" + platform.NewUUIDV7CompactUpper()
}

func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isDeadlockError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func deadlockBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	base := time.Duration(10*(1<<attempt)) * time.Millisecond
	jitter := time.Duration(rand.IntN(25+attempt*10)) * time.Millisecond
	return base + jitter
}
