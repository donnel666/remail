package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
)

type ProtoCheckoutRecoveryRepository interface {
	ListCheckoutAllocationRecoveriesExcludingProtoPaid(context.Context, time.Time, int) ([]CheckoutAllocationRecovery, error)
}

func (uc *UseCase) listCheckoutAllocationRecoveries(ctx context.Context, staleBefore time.Time, limit int) ([]CheckoutAllocationRecovery, error) {
	if !uc.protoProtocolReady() {
		if repo, ok := uc.repo.(ProtoCheckoutRecoveryRepository); ok {
			return repo.ListCheckoutAllocationRecoveriesExcludingProtoPaid(ctx, staleBefore, limit)
		}
	}
	return uc.repo.ListCheckoutAllocationRecoveries(ctx, staleBefore, limit)
}

func (uc *UseCase) protoProtocolReady() bool {
	ready, ok := uc.allocation.(interface{ ProtoProtocolReady() bool })
	return ok && ready.ProtoProtocolReady()
}

func (uc *UseCase) protoFulfillmentReady(ctx context.Context, order domain.Order, allocation *AllocationResult) (bool, error) {
	if order.ProductType != domain.ProductTypeProto && (allocation == nil || allocation.Type != domain.AllocationTypeProto) {
		return true, nil
	}
	if !uc.protoProtocolReady() {
		return false, nil
	}
	ready, ok := uc.allocation.(interface {
		ProtoAllocationReady(context.Context, string, uint) (bool, error)
	})
	if !ok {
		return false, errors.New("proto allocation readiness is unavailable")
	}
	var allocationID uint
	if allocation != nil {
		allocationID = allocation.ID
	}
	return ready.ProtoAllocationReady(ctx, order.OrderNo, allocationID)
}

func (uc *UseCase) compensateUnreadyProtoPaid(ctx context.Context, order domain.Order) (*CheckoutResult, error) {
	if !uc.protoProtocolReady() {
		return &CheckoutResult{Order: order}, domain.ErrProjectUnavailable
	}
	failed, err := uc.compensatePaidCheckout(ctx, order, domain.OrderFailureInsufficientInventory, "Proto mailbox session is unavailable.")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrOrderCompensationError, err)
	}
	if failed == nil {
		return nil, errors.New("refund unavailable Proto order returned no order")
	}
	result := &CheckoutResult{Order: *failed}
	if failed.Status == domain.OrderStatusFailed {
		return result, checkoutInventoryError(*failed, domain.ErrInsufficientInventory)
	}
	return result, nil
}
