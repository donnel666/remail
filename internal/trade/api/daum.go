package api

import (
	"context"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
)

func (a allocationAdapter) DaumProtocolReady() bool {
	return a.alloc != nil && a.alloc.DaumProtocolReady()
}

func (a allocationAdapter) DaumAllocationReady(ctx context.Context, orderNo string, allocationID uint) (bool, error) {
	if a.alloc == nil {
		return false, nil
	}
	return a.alloc.DaumAllocationReady(ctx, orderNo, allocationID)
}

func (a allocationAdapter) ImportHistoricalDaumAllocation(ctx context.Context, cmd tradeapp.HistoricalDaumAllocationCommand) (*tradeapp.AllocationResult, error) {
	if cmd.Mailbox != "main" && cmd.Mailbox != "alias" {
		return nil, domain.ErrInvalidOrderRequest
	}
	result, err := a.alloc.ImportHistoricalDaumAllocation(ctx, allocapp.HistoricalDaumAllocationCommand{
		ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
		Email: cmd.Email, CreatedAt: cmd.CreatedAt, ReleasedAt: cmd.ReleasedAt,
	})
	if err != nil {
		return nil, mapAllocationError(err)
	}
	if result == nil {
		return nil, nil
	}
	return &tradeapp.AllocationResult{
		OrderNo: result.OrderNo, Type: domain.AllocationType(result.Type), ID: result.ID,
		ProductID: result.ProductID, Created: result.Created, Email: result.Email,
		SupplyScope: tradeSupplyScope(result.SupplyScope), CreatedAt: result.CreatedAt, ReleasedAt: result.ReleasedAt,
	}, nil
}
