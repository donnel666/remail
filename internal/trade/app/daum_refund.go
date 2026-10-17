package app

import (
	"context"
	"errors"
	"strings"

	"github.com/donnel666/remail/internal/trade/domain"
)

type ResourceFailureProof struct {
	OrderNo              string
	ResourceID           uint
	CredentialRevision   uint64
	ValidationGeneration uint64
	FailureCategory      string
}

type DaumUnavailableOrder = ResourceFailureProof
type MicrosoftUnavailableOrder = ResourceFailureProof

type DaumUnavailableOrderRepository interface {
	ListUnavailableDaumOrders(context.Context, uint, int) ([]DaumUnavailableOrder, error)
	AssertDaumRefundFailure(context.Context, DaumUnavailableOrder) error
}

func (uc *UseCase) RefundUnavailableDaumOrders(ctx context.Context, resourceID uint, requestID string) (int, error) {
	if uc == nil || uc.repo == nil || uc.wallet == nil || resourceID == 0 {
		return 0, domain.ErrInvalidOrderRequest
	}
	repo, ok := uc.repo.(DaumUnavailableOrderRepository)
	if !ok {
		return 0, domain.ErrInvalidOrderRequest
	}
	orders, err := repo.ListUnavailableDaumOrders(ctx, resourceID, 200)
	if err != nil {
		return 0, err
	}
	refunded := 0
	var failures error
	for _, proof := range orders {
		changed, err := uc.refundUnavailableDaumOrder(ctx, proof, requestID)
		if changed {
			refunded++
		}
		failures = errors.Join(failures, err)
	}
	return refunded, failures
}

func (uc *UseCase) refundUnavailableDaumOrder(ctx context.Context, proof DaumUnavailableOrder, requestID string) (bool, error) {
	repo, ok := uc.repo.(DaumUnavailableOrderRepository)
	if !ok {
		return false, domain.ErrInvalidOrderRequest
	}
	orderNo := proof.OrderNo
	order, changed, err := uc.refundOrder(ctx, refundOrderRequest{
		OrderNo: orderNo, Reason: "Daum resource is permanently unavailable.",
		IdempotencyKey: "order:" + strings.TrimSpace(orderNo) + ":refund", RequestID: strings.TrimSpace(requestID),
		Operator: domain.OperatorTypeSystem, AllowedStatuses: []domain.OrderStatus{domain.OrderStatusActive},
		ReconcileDelivery: true,
		BeforeOrderLock:   func(txCtx context.Context) error { return repo.AssertDaumRefundFailure(txCtx, proof) },
	})
	if errors.Is(err, domain.ErrOrderStateConflict) {
		return false, nil
	}
	if err != nil || order == nil || !changed {
		return false, err
	}
	return true, uc.cleanupOrderService(ctx, *order, true, "Order refunded because its Daum resource is permanently unavailable.", requestID)
}

func (uc *UseCase) expireUnavailableDaumOrders(ctx context.Context, limit int, result *ExpireOrdersResult) error {
	repo, ok := uc.repo.(DaumUnavailableOrderRepository)
	if !ok {
		return nil
	}
	orders, err := repo.ListUnavailableDaumOrders(ctx, 0, limit)
	if err != nil {
		return err
	}
	for _, proof := range orders {
		refunded, err := uc.refundUnavailableDaumOrder(ctx, proof, "")
		if err != nil {
			result.Failed++
		} else if refunded {
			result.ResourceUnavailableRefunded++
		}
	}
	return nil
}
