package infra

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
)

func (r *Repo) ListCheckoutAllocationRecoveriesExcludingProtoPaid(ctx context.Context, staleBefore time.Time, limit int) ([]tradeapp.CheckoutAllocationRecovery, error) {
	return r.listCheckoutAllocationRecoveries(ctx, staleBefore, limit, true)
}

func (r *Repo) ListUnavailableProtoOrderNos(ctx context.Context, resourceID uint, limit int) ([]string, error) {
	var orders []string
	query := r.dbFor(ctx).Table("orders AS o").Select("o.order_no").
		Joins("JOIN proto_allocations pa ON pa.order_no = o.order_no AND pa.guard_type = 'proto' AND pa.status = 'allocated'").
		Joins("JOIN proto_resources pr ON pr.id = pa.resource_id AND pr.resource_type = 'proto' AND pr.status = 'abnormal'").
		Where("o.allocation_type = 'proto' AND o.status = ? AND o.debit_tx_id IS NOT NULL AND o.refund_tx_id IS NULL", string(domain.OrderStatusActive)).
		Order("o.id ASC")
	if resourceID > 0 {
		query = query.Where("pa.resource_id = ?", resourceID)
	}
	if limit <= 0 {
		limit = 200
	}
	if err := query.Limit(limit).Scan(&orders).Error; err != nil {
		return nil, fmt.Errorf("list orders on unavailable Proto resources: %w", err)
	}
	return orders, nil
}

func (r *Repo) CreateHistoricalProtoOrder(ctx context.Context, cmd tradeapp.CreateHistoricalProtoOrderCommand) error {
	orderNo := strings.TrimSpace(cmd.OrderNo)
	deliveryEmail := strings.ToLower(strings.TrimSpace(cmd.DeliveryEmail))
	if orderNo == "" || cmd.UserID == 0 || cmd.ProjectID == 0 || cmd.ProjectProductID == 0 ||
		cmd.ProductType != domain.ProductTypeProto ||
		cmd.DebitTxID == 0 || cmd.AllocationID == 0 || deliveryEmail == "" ||
		cmd.CreatedAt.IsZero() || cmd.ExpiredAt.IsZero() || !cmd.ExpiredAt.Before(cmd.Now) || cmd.ExpiredAt.Before(cmd.CreatedAt) {
		return domain.ErrInvalidOrderRequest
	}
	allocationType := string(domain.AllocationTypeProto)
	debitTxID := cmd.DebitTxID
	createdAt := cmd.CreatedAt.UTC()
	expiredAt := cmd.ExpiredAt.UTC()
	requestFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(orderNo)))
	model := OrderModel{
		OrderNo: orderNo, UserID: cmd.UserID, ProjectID: cmd.ProjectID, ProjectProductID: cmd.ProjectProductID,
		ProductType: string(cmd.ProductType), ServiceMode: string(domain.ServiceModePurchase),
		SupplyPolicy: string(domain.SupplyPolicyPublicOnly), Status: string(domain.OrderStatusCompleted),
		FailureCode: "", PayAmount: "0", RefundAmount: "0",
		CodeWindowMinutes: cmd.CodeWindowMinutes, ActivationWindowMinutes: cmd.ActivationWindowMinutes,
		WarrantyMinutes: cmd.WarrantyMinutes, DebitTxID: &debitTxID, AllocationType: &allocationType,
		DeliveryEmail: deliveryEmail, ReceiveStartedAt: &createdAt, ReceiveUntil: &expiredAt,
		ActivatedAt: &createdAt, AfterSaleUntil: &expiredAt,
		ClientChannel: string(domain.ClientChannelConsole), IdempotencyKey: "history:" + orderNo,
		RequestFingerprint: requestFingerprint, ServiceCleanupStatus: "succeeded",
		CreatedAt: createdAt, UpdatedAt: cmd.Now.UTC(), Version: 1,
	}
	if err := r.dbFor(ctx).Create(&model).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrIdempotencyConflict
		}
		return fmt.Errorf("create historical Proto order: %w", err)
	}
	return nil
}
