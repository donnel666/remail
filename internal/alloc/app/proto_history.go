package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
)

type HistoricalProtoAllocationCommand struct {
	ProjectID  uint
	ProductID  uint
	ResourceID uint
	Email      string
	CreatedAt  time.Time
	ReleasedAt time.Time
}

type ProtoHistoryRepository interface {
	LockProtoHistoricalResource(context.Context, uint) (*ProtoCandidate, error)
	HasProtoProjectHistory(context.Context, uint, uint) (bool, error)
	CreateProtoAllocation(context.Context, *domain.ProtoAllocation) error
}

func (uc *UseCase) ImportHistoricalProtoAllocation(ctx context.Context, cmd HistoricalProtoAllocationCommand) (*domain.UnifiedAllocation, error) {
	cmd.Email = strings.ToLower(strings.TrimSpace(cmd.Email))
	cmd.CreatedAt, cmd.ReleasedAt = cmd.CreatedAt.UTC(), cmd.ReleasedAt.UTC()
	if uc == nil || uc.repo == nil || cmd.ProjectID == 0 || cmd.ProductID == 0 || cmd.ResourceID == 0 ||
		cmd.Email == "" || cmd.CreatedAt.IsZero() || cmd.ReleasedAt.IsZero() || cmd.ReleasedAt.Before(cmd.CreatedAt) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	repo, ok := uc.repo.(ProtoHistoryRepository)
	if !ok {
		return nil, fmt.Errorf("proto history repository is unavailable")
	}
	var result *domain.UnifiedAllocation
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		locked, err := uc.repo.LockResourceRoot(txCtx, cmd.ResourceID, domain.AllocationTypeProto)
		if err != nil {
			return err
		}
		if !locked {
			return domain.ErrInvalidAllocationRequest
		}
		resource, err := repo.LockProtoHistoricalResource(txCtx, cmd.ResourceID)
		if err != nil {
			return err
		}
		if resource == nil || resource.OwnerUserID == 0 || !strings.EqualFold(resource.Email, cmd.Email) {
			return domain.ErrInvalidAllocationRequest
		}
		orderNo := fmt.Sprintf("HIST-PROTO-%d-%d", cmd.ResourceID, cmd.ProjectID)
		existing, err := uc.repo.FindExistingAllocation(txCtx, orderNo)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Type != domain.AllocationTypeProto || existing.ResourceID != cmd.ResourceID ||
				existing.ProjectID != cmd.ProjectID || existing.ProductID != cmd.ProductID || existing.Mailbox != "main" ||
				existing.Status != domain.AllocationStatusReleased || !strings.EqualFold(existing.Email, cmd.Email) {
				return domain.ErrAllocationConflict
			}
			result = existing
			return nil
		}
		matched, err := repo.HasProtoProjectHistory(txCtx, cmd.ResourceID, cmd.ProjectID)
		if err != nil || matched {
			return err
		}
		if err := uc.repo.CreateOrderGuard(txCtx, orderNo, domain.AllocationTypeProto); err != nil {
			return err
		}
		releasedAt := cmd.ReleasedAt
		allocation := &domain.ProtoAllocation{
			OrderNo: orderNo, ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
			OwnerUserID: resource.OwnerUserID, SupplyScope: domain.SupplyScopePublic, Mailbox: "main",
			ServiceMode: string(domain.ServiceModePurchase), Email: cmd.Email,
			Status: domain.AllocationStatusReleased, CostPointsSnapshot: "0.00",
			CreatedAt: cmd.CreatedAt, ReleasedAt: &releasedAt,
		}
		if err := repo.CreateProtoAllocation(txCtx, allocation); err != nil {
			return err
		}
		result = &domain.UnifiedAllocation{
			Type: domain.AllocationTypeProto, ID: allocation.ID, OrderNo: orderNo,
			ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
			SupplyScope: domain.SupplyScopePublic, Mailbox: "main", Email: cmd.Email,
			Status: domain.AllocationStatusReleased, CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt, Created: true,
		}
		return nil
	})
	return result, err
}
