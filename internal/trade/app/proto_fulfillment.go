package app

import (
	"context"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
)

type ProtoCheckoutRecoveryRepository interface {
	ListCheckoutAllocationRecoveriesExcludingProtoPaid(context.Context, time.Time, int) ([]CheckoutAllocationRecovery, error)
}

func (uc *UseCase) listCheckoutAllocationRecoveries(ctx context.Context, staleBefore time.Time, limit int) ([]CheckoutAllocationRecovery, error) {
	if !uc.protoFulfillmentReady(domain.Order{ProductType: domain.ProductTypeProto}, nil) {
		if repo, ok := uc.repo.(ProtoCheckoutRecoveryRepository); ok {
			return repo.ListCheckoutAllocationRecoveriesExcludingProtoPaid(ctx, staleBefore, limit)
		}
	}
	return uc.repo.ListCheckoutAllocationRecoveries(ctx, staleBefore, limit)
}

func (uc *UseCase) protoFulfillmentReady(order domain.Order, allocation *AllocationResult) bool {
	if order.ProductType != domain.ProductTypeProto && (allocation == nil || allocation.Type != domain.AllocationTypeProto) {
		return true
	}
	ready, ok := uc.allocation.(interface{ ProtoProtocolReady() bool })
	return ok && ready.ProtoProtocolReady()
}
