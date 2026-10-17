package infra

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
)

func (r *Repo) CreateHistoricalDaumOrder(ctx context.Context, cmd tradeapp.CreateHistoricalDaumOrderCommand) error {
	orderNo := strings.TrimSpace(cmd.OrderNo)
	deliveryEmail := strings.ToLower(strings.TrimSpace(cmd.DeliveryEmail))
	if orderNo == "" || cmd.UserID == 0 || cmd.ProjectID == 0 || cmd.ProjectProductID == 0 ||
		cmd.ProductType != domain.ProductTypeDaum ||
		cmd.DebitTxID == 0 || cmd.AllocationID == 0 || deliveryEmail == "" ||
		cmd.CreatedAt.IsZero() || cmd.ExpiredAt.IsZero() || !cmd.ExpiredAt.Before(cmd.Now) || cmd.ExpiredAt.Before(cmd.CreatedAt) {
		return domain.ErrInvalidOrderRequest
	}
	allocationType := string(domain.AllocationTypeDaum)
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
		return fmt.Errorf("create historical Daum order: %w", err)
	}
	return nil
}
