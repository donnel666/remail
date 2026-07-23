package infra

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
	"unicode"

	moneyfmt "github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OrderModel struct {
	ID                      uint       `gorm:"primaryKey;autoIncrement"`
	OrderNo                 string     `gorm:"type:varchar(64);not null;column:order_no"`
	UserID                  uint       `gorm:"not null;column:user_id"`
	ProjectID               uint       `gorm:"not null;column:project_id"`
	ProjectProductID        uint       `gorm:"not null;column:project_product_id"`
	ProductType             string     `gorm:"type:varchar(32);not null;column:product_type"`
	ServiceMode             string     `gorm:"type:varchar(32);not null;column:service_mode"`
	SupplyPolicy            string     `gorm:"type:varchar(32);not null;column:supply_policy"`
	Status                  string     `gorm:"type:varchar(32);not null"`
	FailureCode             string     `gorm:"type:varchar(32);not null;default:'';column:failure_code"`
	PayAmount               string     `gorm:"type:decimal(18,6);not null;column:pay_amount"`
	RefundAmount            string     `gorm:"type:decimal(18,6);not null;column:refund_amount"`
	CodeWindowMinutes       int        `gorm:"not null;column:code_window_minutes"`
	ActivationWindowMinutes int        `gorm:"not null;column:activation_window_minutes"`
	WarrantyMinutes         int        `gorm:"not null;column:warranty_minutes"`
	DebitTxID               *uint      `gorm:"column:debit_tx_id"`
	RefundTxID              *uint      `gorm:"column:refund_tx_id"`
	AllocationType          *string    `gorm:"type:varchar(32);column:allocation_type"`
	MicrosoftAllocID        *uint      `gorm:"column:microsoft_alloc_id"`
	DomainAllocID           *uint      `gorm:"column:domain_alloc_id"`
	DeliveryEmail           string     `gorm:"type:varchar(255);not null;column:delivery_email"`
	ReceiveStartedAt        *time.Time `gorm:"column:receive_started_at"`
	ReceiveUntil            *time.Time `gorm:"column:receive_until"`
	ActivatedAt             *time.Time `gorm:"column:activated_at"`
	AfterSaleUntil          *time.Time `gorm:"column:after_sale_until"`
	ClientChannel           string     `gorm:"type:varchar(32);not null;column:client_channel"`
	APIKeyID                *uint      `gorm:"column:api_key_id"`
	IdempotencyKey          string     `gorm:"type:varchar(128);not null;column:idempotency_key"`
	RequestFingerprint      string     `gorm:"type:char(64);not null;column:request_fingerprint"`
	ServiceCleanupStatus    string     `gorm:"type:varchar(32);not null;column:service_cleanup_status"`
	ArchivedAt              *time.Time `gorm:"column:archived_at"`
	CreatedAt               time.Time  `gorm:"not null;autoCreateTime;column:created_at"`
	UpdatedAt               time.Time  `gorm:"not null;autoUpdateTime;column:updated_at"`
	Version                 int        `gorm:"not null;default:1"`
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
	for attempt := 0; attempt < 2; attempt++ {
		// Allocation runs inside this parent transaction, so it needs the same
		// statement-level snapshots as Allocation's own top-level transaction.
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return fn(platform.WithGormTx(ctx, tx))
		}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err == nil || !isDeadlockError(err) {
			return err
		}
		platform.RecordMySQLTransactionEvent("trade", mysqlRetryEvent(err))
		if !isDeadlockVictim(err) {
			return err
		}
		if attempt == 1 {
			platform.RecordMySQLTransactionEvent("trade", "retry_exhausted")
			return err
		}
		platform.RecordMySQLTransactionEvent("trade", "retry")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(deadlockBackoff(attempt)):
		}
	}
	return err
}

func (r *Repo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

func (r *Repo) FindHistoricalOrderOwner(ctx context.Context) (uint, error) {
	var owner struct {
		ID uint `gorm:"column:id"`
	}
	if err := r.dbFor(ctx).Raw(`
SELECT id
FROM users
WHERE role = 'super_admin'
ORDER BY id ASC
LIMIT 1
FOR SHARE`).Scan(&owner).Error; err != nil {
		return 0, fmt.Errorf("find historical order owner: %w", err)
	}
	if owner.ID == 0 {
		return 0, nil
	}
	return owner.ID, nil
}

func (r *Repo) CreateHistoricalOrder(ctx context.Context, cmd tradeapp.CreateHistoricalOrderCommand) error {
	orderNo := strings.TrimSpace(cmd.OrderNo)
	deliveryEmail := strings.ToLower(strings.TrimSpace(cmd.DeliveryEmail))
	if orderNo == "" || cmd.UserID == 0 || cmd.ProjectID == 0 || cmd.ProjectProductID == 0 ||
		cmd.DebitTxID == 0 || cmd.MicrosoftAllocationID == 0 || deliveryEmail == "" ||
		cmd.CreatedAt.IsZero() || cmd.ExpiredAt.IsZero() || !cmd.ExpiredAt.Before(cmd.Now) {
		return domain.ErrInvalidOrderRequest
	}
	allocationType := string(domain.AllocationTypeMicrosoft)
	debitTxID := cmd.DebitTxID
	allocationID := cmd.MicrosoftAllocationID
	createdAt := cmd.CreatedAt.UTC()
	expiredAt := cmd.ExpiredAt.UTC()
	requestFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(orderNo)))
	model := OrderModel{
		OrderNo: orderNo, UserID: cmd.UserID, ProjectID: cmd.ProjectID, ProjectProductID: cmd.ProjectProductID,
		ProductType: string(domain.ProductTypeMicrosoft), ServiceMode: string(domain.ServiceModePurchase),
		SupplyPolicy: string(domain.SupplyPolicyPublicOnly), Status: string(domain.OrderStatusCompleted),
		FailureCode: "", PayAmount: "0", RefundAmount: "0",
		CodeWindowMinutes: cmd.CodeWindowMinutes, ActivationWindowMinutes: cmd.ActivationWindowMinutes,
		WarrantyMinutes: cmd.WarrantyMinutes, DebitTxID: &debitTxID,
		AllocationType: &allocationType, MicrosoftAllocID: &allocationID,
		DeliveryEmail: deliveryEmail, ReceiveStartedAt: &createdAt, ReceiveUntil: &expiredAt,
		ActivatedAt: &createdAt, AfterSaleUntil: &expiredAt,
		ClientChannel: string(domain.ClientChannelConsole), IdempotencyKey: "history:" + orderNo,
		RequestFingerprint: requestFingerprint, ServiceCleanupStatus: "succeeded",
		CreatedAt: createdAt, UpdatedAt: cmd.Now.UTC(), Version: 1,
	}
	tx := r.dbFor(ctx)
	if err := tx.Create(&model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrIdempotencyConflict
		}
		return fmt.Errorf("create historical order: %w", err)
	}
	return r.appendEvent(
		ctx,
		tx,
		orderNo,
		"order.history_imported",
		nil,
		ptrStatus(domain.OrderStatusCompleted),
		domain.OperatorTypeSystem,
		"Historical Microsoft mailbox usage identified.",
	)
}

func (r *Repo) LoadOrCreatePendingOrder(ctx context.Context, cmd tradeapp.CreatePendingOrderCommand) (*domain.Order, bool, error) {
	var model OrderModel
	created := false
	err := r.WithTx(ctx, func(txCtx context.Context) error {
		tx := r.dbFor(txCtx)
		candidate := OrderModel{
			OrderNo:                 cmd.OrderNo,
			UserID:                  cmd.UserID,
			ProjectID:               cmd.ProjectID,
			ProjectProductID:        cmd.ProjectProductID,
			ProductType:             string(cmd.ProductType),
			ServiceMode:             string(cmd.ServiceMode),
			SupplyPolicy:            string(cmd.SupplyPolicy),
			Status:                  string(domain.OrderStatusPendingPayment),
			FailureCode:             "",
			PayAmount:               cmd.PayAmount,
			RefundAmount:            "0.00",
			CodeWindowMinutes:       cmd.CodeWindowMinutes,
			ActivationWindowMinutes: cmd.ActivationWindowMinutes,
			WarrantyMinutes:         cmd.WarrantyMinutes,
			ClientChannel:           string(cmd.ClientChannel),
			APIKeyID:                cmd.APIKeyID,
			IdempotencyKey:          strings.TrimSpace(cmd.IdempotencyKey),
			RequestFingerprint:      cmd.RequestFingerprint,
			ServiceCleanupStatus:    "none",
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

func (r *Repo) FindOrderByIdempotency(ctx context.Context, channel domain.ClientChannel, userID uint, apiKeyID *uint, idempotencyKey, requestFingerprint string) (*domain.Order, error) {
	var model OrderModel
	err := r.dbFor(ctx).
		Where("idempotency_subject = ? AND idempotency_key = ?", idempotencySubject(channel, userID, apiKeyID), strings.TrimSpace(idempotencyKey)).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find idempotent order: %w", err)
	}
	if model.RequestFingerprint != requestFingerprint {
		return nil, domain.ErrIdempotencyConflict
	}
	order := orderModelToDomain(model)
	return &order, nil
}

func (r *Repo) FindOrdersByIdempotencyBatch(
	ctx context.Context,
	channel domain.ClientChannel,
	userID uint,
	apiKeyID *uint,
	idempotencyKeys []string,
) (map[string]domain.Order, error) {
	result := make(map[string]domain.Order, len(idempotencyKeys))
	if len(idempotencyKeys) == 0 {
		return result, nil
	}
	keys := make([]string, 0, len(idempotencyKeys))
	seen := make(map[string]struct{}, len(idempotencyKeys))
	for _, key := range idempotencyKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return result, nil
	}
	var models []OrderModel
	if err := r.dbFor(ctx).
		Where("idempotency_subject = ? AND idempotency_key IN ?", idempotencySubject(channel, userID, apiKeyID), keys).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("find idempotent order batch: %w", err)
	}
	for _, model := range models {
		result[model.IdempotencyKey] = orderModelToDomain(model)
	}
	return result, nil
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
			"status":       string(domain.OrderStatusFailed),
			"failure_code": string(normalizeFailureCode(cmd.FailureCode)),
			"version":      gorm.Expr("version + 1"),
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
		Updates(map[string]any{
			"service_cleanup_status": status,
			"updated_at":             time.Now().UTC(),
		})
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

func (r *Repo) ListOrders(ctx context.Context, filter tradeapp.OrderListFilter, offset int, afterID uint, limit int) ([]domain.Order, *uint, error) {
	query := applyOrderFilter(r.dbFor(ctx).Model(&OrderModel{}), filter)
	if afterID > 0 {
		query = query.Where("id < ?", afterID)
	} else if offset > 0 {
		query = query.Offset(offset)
	}
	var models []OrderModel
	if err := query.
		Select("orders.*").
		Order("id DESC").
		Limit(limit + 1).
		Find(&models).Error; err != nil {
		return nil, nil, fmt.Errorf("list orders: %w", err)
	}
	var nextAfterID *uint
	if len(models) > limit {
		models = models[:limit]
		next := models[len(models)-1].ID
		nextAfterID = &next
	}
	items := make([]domain.Order, len(models))
	for i := range models {
		items[i] = orderModelToDomain(models[i])
	}
	return items, nextAfterID, nil
}

func (r *Repo) CountOrders(ctx context.Context, filter tradeapp.OrderListFilter) (int64, error) {
	var total int64
	if err := applyOrderFilter(r.dbFor(ctx).Model(&OrderModel{}), filter).
		Count(&total).Error; err != nil {
		return 0, fmt.Errorf("count orders: %w", err)
	}
	return total, nil
}

// OrderFacets computes list aggregates; each dimension excludes its own
// filter value so the console can render selectable counts.
func (r *Repo) OrderFacets(ctx context.Context, filter tradeapp.OrderListFilter) (*tradeapp.OrderListFacets, error) {
	facets := &tradeapp.OrderListFacets{}

	statusBase := filter
	statusBase.Status = ""
	var statusRow struct {
		All            int64 `gorm:"column:all_count"`
		PendingPayment int64 `gorm:"column:pending_payment_count"`
		Paid           int64 `gorm:"column:paid_count"`
		Active         int64 `gorm:"column:active_count"`
		Completed      int64 `gorm:"column:completed_count"`
		Refunded       int64 `gorm:"column:refunded_count"`
		Failed         int64 `gorm:"column:failed_count"`
		Closed         int64 `gorm:"column:closed_count"`
	}
	if err := applyOrderFilter(r.dbFor(ctx).Model(&OrderModel{}), statusBase).
		Select(
			`COUNT(*) AS all_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS pending_payment_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS paid_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS active_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS completed_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS refunded_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS failed_count,
			COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS closed_count`,
			string(domain.OrderStatusPendingPayment),
			string(domain.OrderStatusPaid),
			string(domain.OrderStatusActive),
			string(domain.OrderStatusCompleted),
			string(domain.OrderStatusRefunded),
			string(domain.OrderStatusFailed),
			string(domain.OrderStatusClosed),
		).
		Scan(&statusRow).Error; err != nil {
		return nil, fmt.Errorf("order status facets: %w", err)
	}
	facets.Status = tradeapp.OrderStatusFacets{
		All:            statusRow.All,
		PendingPayment: statusRow.PendingPayment,
		Paid:           statusRow.Paid,
		Active:         statusRow.Active,
		Completed:      statusRow.Completed,
		Refunded:       statusRow.Refunded,
		Failed:         statusRow.Failed,
		Closed:         statusRow.Closed,
	}

	modeBase := filter
	modeBase.ServiceMode = ""
	var modeRow struct {
		All      int64 `gorm:"column:all_count"`
		Code     int64 `gorm:"column:code_count"`
		Purchase int64 `gorm:"column:purchase_count"`
	}
	if err := applyOrderFilter(r.dbFor(ctx).Model(&OrderModel{}), modeBase).
		Select(
			`COUNT(*) AS all_count,
			COALESCE(SUM(CASE WHEN service_mode = ? THEN 1 ELSE 0 END), 0) AS code_count,
			COALESCE(SUM(CASE WHEN service_mode = ? THEN 1 ELSE 0 END), 0) AS purchase_count`,
			string(domain.ServiceModeCode),
			string(domain.ServiceModePurchase),
		).
		Scan(&modeRow).Error; err != nil {
		return nil, fmt.Errorf("order service mode facets: %w", err)
	}
	facets.ServiceMode = tradeapp.OrderServiceModeFacets{
		All:      modeRow.All,
		Code:     modeRow.Code,
		Purchase: modeRow.Purchase,
	}

	domainBase := filter
	domainBase.Domain = ""
	type keyRow struct {
		Key   string `gorm:"column:facet_key"`
		Count int64  `gorm:"column:count"`
	}
	rows := make([]keyRow, 0)
	if err := applyOrderFilter(r.dbFor(ctx).Model(&OrderModel{}), domainBase).
		Select("SUBSTRING_INDEX(delivery_email, '@', -1) AS facet_key, COUNT(*) AS count").
		Where("delivery_email LIKE ?", "%@%").
		Group("facet_key").
		Order("count DESC, facet_key ASC").
		Limit(100).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("order domain facets: %w", err)
	}
	facets.Domains = make([]tradeapp.OrderKeyFacet, len(rows))
	for i := range rows {
		facets.Domains[i] = tradeapp.OrderKeyFacet{Key: rows[i].Key, Count: rows[i].Count}
	}
	return facets, nil
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

func (r *Repo) ListPartialCleanupOrderNos(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	var orderNos []string
	if err := r.dbFor(ctx).Model(&OrderModel{}).
		Where("service_cleanup_status = ? AND (status IN ? OR (status = ? AND refund_tx_id IS NOT NULL))",
			"partial_failure",
			[]string{string(domain.OrderStatusRefunded), string(domain.OrderStatusClosed)},
			string(domain.OrderStatusFailed),
		).
		Order("updated_at ASC, id ASC").
		Limit(limit).
		Pluck("order_no", &orderNos).Error; err != nil {
		return nil, fmt.Errorf("list partial cleanup order numbers: %w", err)
	}
	return orderNos, nil
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
		if current != domain.OrderStatusActive && current != domain.OrderStatusCompleted {
			return nil
		}
		if model.ActivatedAt != nil {
			return nil
		}
		previous := current
		updates := map[string]any{
			"activated_at":     matchedAt.UTC(),
			"after_sale_until": afterSaleUntil.UTC(),
			"version":          gorm.Expr("version + 1"),
		}
		result := tx.Model(&OrderModel{}).
			Where("order_no = ? AND service_mode = ? AND status IN ?", orderNo, string(domain.ServiceModePurchase), []string{
				string(domain.OrderStatusActive),
				string(domain.OrderStatusCompleted),
			}).
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
		reason := "Purchase activated by matched mail at " + matchedAt.UTC().Format(time.RFC3339)
		return r.appendEvent(txCtx, tx, orderNo, "order.purchase_activated", &previous, &previous, domain.OperatorTypeSystem, reason)
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

var likePatternEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLikePattern(value string) string {
	return likePatternEscaper.Replace(value)
}

func applyOrderFilter(query *gorm.DB, filter tradeapp.OrderListFilter) *gorm.DB {
	query = query.Where("order_no NOT LIKE 'HIST-%'")
	if !filter.IsAdmin || filter.Scope != "all" {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", string(filter.Status))
	}
	if filter.ServiceMode != "" {
		query = query.Where("service_mode = ?", string(filter.ServiceMode))
	}
	if domainFilter := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(filter.Domain)), "@"); domainFilter != "" {
		query = query.Where("delivery_email LIKE ?", "%@"+escapeLikePattern(domainFilter))
	}
	if filter.CreatedFrom != nil {
		query = query.Where("created_at >= ?", filter.CreatedFrom.UTC())
	}
	if filter.CreatedTo != nil {
		query = query.Where("created_at <= ?", filter.CreatedTo.UTC())
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		query = applyOrderSearch(query, search)
	}
	return query
}

func applyOrderSearch(query *gorm.DB, search string) *gorm.DB {
	like := escapeLikePattern(search) + "%"
	parts := []string{
		"SELECT id AS order_id FROM orders WHERE order_no LIKE ?",
		"SELECT id FROM orders WHERE delivery_email LIKE ?",
		"SELECT o.id FROM users u JOIN orders o ON o.user_id = u.id WHERE u.email = ?",
	}
	args := []any{like, like, strings.ToLower(search)}
	if userSearch := orderSearchBooleanQuery(search, 3); userSearch != "" {
		parts = append(parts, "SELECT o.id FROM users u JOIN orders o ON o.user_id = u.id WHERE MATCH(u.email, u.nickname) AGAINST (? IN BOOLEAN MODE)")
		args = append(args, userSearch)
	}
	if projectSearch := orderSearchBooleanQuery(search, 1); projectSearch != "" {
		parts = append(parts, "SELECT o.id FROM projects p JOIN orders o ON o.project_id = p.id WHERE MATCH(p.name, p.target_platform) AGAINST (? IN BOOLEAN MODE)")
		args = append(args, projectSearch)
	}
	if id, err := strconv.ParseUint(search, 10, 64); err == nil && id > 0 {
		parts = append(parts,
			"SELECT id FROM orders WHERE user_id = ?",
			"SELECT id FROM orders WHERE project_id = ?",
		)
		args = append(args, id, id)
	}
	// ponytail: UNION materializes broad matches; add a persisted order search index only if profiles require it.
	return query.Table(
		"("+strings.Join(parts, " UNION ")+") AS order_search STRAIGHT_JOIN orders FORCE INDEX (PRIMARY) ON orders.id = order_search.order_id",
		args...,
	)
}

func orderSearchBooleanQuery(search string, minRunes int) string {
	parts := strings.FieldsFunc(strings.ToLower(search), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	terms := make([]string, 0, len(parts))
	for _, part := range parts {
		if len([]rune(part)) >= minRunes {
			terms = append(terms, "+"+part+"*")
		}
	}
	return strings.Join(terms, " ")
}

func orderModelToDomain(model OrderModel) domain.Order {
	var allocationType *domain.AllocationType
	if model.AllocationType != nil {
		value := domain.AllocationType(*model.AllocationType)
		allocationType = &value
	}
	return domain.Order{
		ID:                      model.ID,
		OrderNo:                 model.OrderNo,
		UserID:                  model.UserID,
		ProjectID:               model.ProjectID,
		ProjectProductID:        model.ProjectProductID,
		ProductType:             domain.ProductType(model.ProductType),
		ServiceMode:             domain.ServiceMode(model.ServiceMode),
		SupplyPolicy:            domain.SupplyPolicy(model.SupplyPolicy),
		Status:                  domain.OrderStatus(model.Status),
		FailureCode:             domain.OrderFailureCode(model.FailureCode),
		PayAmount:               normalizeStoredMoney(model.PayAmount),
		RefundAmount:            normalizeStoredMoney(model.RefundAmount),
		CodeWindowMinutes:       model.CodeWindowMinutes,
		ActivationWindowMinutes: model.ActivationWindowMinutes,
		WarrantyMinutes:         model.WarrantyMinutes,
		DebitTxID:               model.DebitTxID,
		RefundTxID:              model.RefundTxID,
		AllocationType:          allocationType,
		MicrosoftAllocID:        model.MicrosoftAllocID,
		DomainAllocID:           model.DomainAllocID,
		DeliveryEmail:           model.DeliveryEmail,
		ReceiveStartedAt:        model.ReceiveStartedAt,
		ReceiveUntil:            model.ReceiveUntil,
		ActivatedAt:             model.ActivatedAt,
		AfterSaleUntil:          model.AfterSaleUntil,
		ClientChannel:           domain.ClientChannel(model.ClientChannel),
		APIKeyID:                model.APIKeyID,
		IdempotencyKey:          model.IdempotencyKey,
		RequestFingerprint:      model.RequestFingerprint,
		ServiceCleanupStatus:    model.ServiceCleanupStatus,
		ArchivedAt:              model.ArchivedAt,
		CreatedAt:               model.CreatedAt,
		UpdatedAt:               model.UpdatedAt,
		Version:                 model.Version,
	}
}

func normalizeStoredMoney(value string) string {
	normalized, err := moneyfmt.Normalize(value)
	if err != nil {
		return value
	}
	return normalized
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

func normalizeFailureCode(code domain.OrderFailureCode) domain.OrderFailureCode {
	switch code {
	case domain.OrderFailureInsufficientInventory,
		domain.OrderFailureInsufficientBalance,
		domain.OrderFailureAllocation,
		domain.OrderFailureServiceToken,
		domain.OrderFailureActivation:
		return code
	default:
		return domain.OrderFailureUnknown
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

func isDeadlockVictim(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}

func mysqlRetryEvent(err error) string {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1205 {
		return "1205"
	}
	return "1213"
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
