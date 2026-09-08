package api

import (
	"context"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
)

func (a allocationAdapter) ProtoProtocolReady() bool {
	return a.alloc != nil && a.alloc.ProtoProtocolReady()
}

func (a allocationAdapter) ProtoAllocationReady(ctx context.Context, orderNo string, allocationID uint) (bool, error) {
	if a.alloc == nil {
		return false, nil
	}
	return a.alloc.ProtoAllocationReady(ctx, orderNo, allocationID)
}

func (a allocationAdapter) ImportHistoricalProtoAllocation(ctx context.Context, cmd tradeapp.HistoricalProtoAllocationCommand) (*tradeapp.AllocationResult, error) {
	if cmd.Mailbox != "main" {
		return nil, domain.ErrInvalidOrderRequest
	}
	result, err := a.alloc.ImportHistoricalProtoAllocation(ctx, allocapp.HistoricalProtoAllocationCommand{
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
