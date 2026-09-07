package app

import (
	"context"
	"errors"
	"strings"

	"github.com/donnel666/remail/internal/trade/domain"
)

type ProtoUnavailableOrderRepository interface {
	ListUnavailableProtoOrderNos(context.Context, uint, int) ([]string, error)
}

func (uc *UseCase) RefundUnavailableProtoOrders(ctx context.Context, resourceID uint, requestID string) (int, error) {
	if uc == nil || uc.repo == nil || uc.wallet == nil || resourceID == 0 {
		return 0, domain.ErrInvalidOrderRequest
	}
	repo, ok := uc.repo.(ProtoUnavailableOrderRepository)
	if !ok {
		return 0, domain.ErrInvalidOrderRequest
	}
	orders, err := repo.ListUnavailableProtoOrderNos(ctx, resourceID, 200)
	if err != nil {
		return 0, err
	}
	refunded := 0
	var failures error
	for _, orderNo := range orders {
		changed, err := uc.refundUnavailableProtoOrder(ctx, orderNo, requestID)
		if changed {
			refunded++
		}
		failures = errors.Join(failures, err)
	}
	return refunded, failures
}

func (uc *UseCase) refundUnavailableProtoOrder(ctx context.Context, orderNo, requestID string) (bool, error) {
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo: orderNo, Reason: "Proto resource is permanently unavailable.",
		IdempotencyKey: "order:" + strings.TrimSpace(orderNo) + ":refund", RequestID: strings.TrimSpace(requestID),
		Operator: domain.OperatorTypeSystem, AllowedStatuses: []domain.OrderStatus{domain.OrderStatusActive},
		ReconcileDelivery: true,
	})
	if errors.Is(err, domain.ErrOrderStateConflict) {
		return false, nil
	}
	if err != nil || order == nil || !changed {
		return false, err
	}
	return true, uc.cleanupOrderService(ctx, *order, true, "Order refunded because its Proto resource is permanently unavailable.", requestID)
}

func (uc *UseCase) expireUnavailableProtoOrders(ctx context.Context, limit int, result *ExpireOrdersResult) error {
	repo, ok := uc.repo.(ProtoUnavailableOrderRepository)
	if !ok {
		return nil
	}
	orders, err := repo.ListUnavailableProtoOrderNos(ctx, 0, limit)
	if err != nil {
		return err
	}
	for _, orderNo := range orders {
		refunded, err := uc.refundUnavailableProtoOrder(ctx, orderNo, "")
		if err != nil {
			result.Failed++
		} else if refunded {
			result.ResourceUnavailableRefunded++
		}
	}
	return nil
}
