package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/donnel666/remail/internal/trade/domain"
)

func (uc *UseCase) daumProtocolReady() bool {
	ready, ok := uc.allocation.(interface{ DaumProtocolReady() bool })
	return ok && ready.DaumProtocolReady()
}

func (uc *UseCase) daumFulfillmentReady(ctx context.Context, order domain.Order, allocation *AllocationResult) (bool, error) {
	if order.ProductType != domain.ProductTypeDaum && (allocation == nil || allocation.Type != domain.AllocationTypeDaum) {
		return true, nil
	}
	if !uc.daumProtocolReady() {
		return false, nil
	}
	ready, ok := uc.allocation.(interface {
		DaumAllocationReady(context.Context, string, uint) (bool, error)
	})
	if !ok {
		return false, errors.New("daum allocation readiness is unavailable")
	}
	var allocationID uint
	if allocation != nil {
		allocationID = allocation.ID
	}
	return ready.DaumAllocationReady(ctx, order.OrderNo, allocationID)
}

func (uc *UseCase) compensateUnreadyDaumPaid(ctx context.Context, order domain.Order) (*CheckoutResult, error) {
	if !uc.daumProtocolReady() {
		return &CheckoutResult{Order: order}, domain.ErrProjectUnavailable
	}
	failed, err := uc.compensatePaidCheckout(ctx, order, domain.OrderFailureInsufficientInventory, "Daum mailbox is unavailable.")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, err)
	}
	if failed == nil {
		return nil, errors.New("refund unavailable Daum order returned no order")
	}
	result := &CheckoutResult{Order: *failed}
	if failed.Status == domain.OrderStatusFailed {
		return result, checkoutInventoryError(*failed, domain.ErrInsufficientInventory)
	}
	return result, nil
}
