package app

import (
	"context"
	"errors"
	"strings"

	"github.com/donnel666/remail/internal/trade/domain"
)

type MicrosoftUnavailableOrderRepository interface {
	ListUnavailableMicrosoftOrders(context.Context, uint, int) ([]MicrosoftUnavailableOrder, error)
	AssertMicrosoftRefundFailure(context.Context, MicrosoftUnavailableOrder) error
}

func (uc *UseCase) RefundUnavailableMicrosoftOrders(ctx context.Context, resourceID uint, requestID string) (int, error) {
	if uc == nil || uc.repo == nil || uc.wallet == nil || resourceID == 0 {
		return 0, domain.ErrInvalidOrderRequest
	}
	repo, ok := uc.repo.(MicrosoftUnavailableOrderRepository)
	if !ok {
		return 0, domain.ErrInvalidOrderRequest
	}
	orders, err := repo.ListUnavailableMicrosoftOrders(ctx, resourceID, 200)
	if err != nil {
		return 0, err
	}
	refunded := 0
	var failures error
	for _, proof := range orders {
		changed, err := uc.refundUnavailableMicrosoftOrder(ctx, proof, requestID)
		if changed {
			refunded++
		}
		failures = errors.Join(failures, err)
	}
	return refunded, failures
}

func (uc *UseCase) refundUnavailableMicrosoftOrder(ctx context.Context, proof MicrosoftUnavailableOrder, requestID string) (bool, error) {
	repo, ok := uc.repo.(MicrosoftUnavailableOrderRepository)
	if !ok {
		return false, domain.ErrInvalidOrderRequest
	}
	orderNo := proof.OrderNo
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo: orderNo, Reason: "Microsoft resource is permanently unavailable.",
		IdempotencyKey: "order:" + strings.TrimSpace(orderNo) + ":refund", RequestID: strings.TrimSpace(requestID),
		Operator: domain.OperatorTypeSystem, AllowedStatuses: []domain.OrderStatus{domain.OrderStatusActive},
		ReconcileDelivery: true,
		BeforeOrderLock:   func(txCtx context.Context) error { return repo.AssertMicrosoftRefundFailure(txCtx, proof) },
	})
	if errors.Is(err, domain.ErrOrderStateConflict) {
		return false, nil
	}
	if err != nil || order == nil || !changed {
		return false, err
	}
	return true, uc.cleanupOrderService(ctx, *order, true, "Order refunded because its Microsoft resource is permanently unavailable.", requestID)
}

func (uc *UseCase) expireUnavailableMicrosoftEnterpriseOrders(ctx context.Context, limit int, result *ExpireOrdersResult) error {
	repo, ok := uc.repo.(interface {
		ListUnavailableMicrosoftEnterpriseOrders(context.Context, int) ([]MicrosoftUnavailableOrder, error)
	})
	if !ok {
		return nil
	}
	orders, err := repo.ListUnavailableMicrosoftEnterpriseOrders(ctx, limit)
	if err != nil {
		return err
	}
	return uc.refundUnavailableMicrosoftOrders(ctx, orders, result)
}
func (uc *UseCase) refundUnavailableMicrosoftOrders(ctx context.Context, orders []MicrosoftUnavailableOrder, result *ExpireOrdersResult) error {
	for _, proof := range orders {
		refunded, err := uc.refundUnavailableMicrosoftOrder(ctx, proof, "")
		if err != nil {
			result.Failed++
		} else if refunded {
			result.ResourceUnavailableRefunded++
		}
	}
	return nil
}

func (uc *UseCase) expireUnavailableMicrosoftOrders(ctx context.Context, limit int, result *ExpireOrdersResult) error {
	repo, ok := uc.repo.(MicrosoftUnavailableOrderRepository)
	if !ok {
		return nil
	}
	orders, err := repo.ListUnavailableMicrosoftOrders(ctx, 0, limit)
	if err != nil {
		return err
	}
	return uc.refundUnavailableMicrosoftOrders(ctx, orders, result)
}
