package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
)

type HistoricalDaumAllocationCommand struct {
	ProjectID  uint
	ProductID  uint
	ResourceID uint
	Email      string
	CreatedAt  time.Time
	ReleasedAt time.Time
}

type DaumHistoryRepository interface {
	LockDaumHistoricalResource(context.Context, uint, string) (*DaumCandidate, error)
	HasDaumProjectHistory(context.Context, uint, uint, string) (bool, error)
	CreateDaumAllocation(context.Context, *domain.DaumAllocation) error
}

func (uc *UseCase) ImportHistoricalDaumAllocation(ctx context.Context, cmd HistoricalDaumAllocationCommand) (*domain.UnifiedAllocation, error) {
	cmd.Email = strings.ToLower(strings.TrimSpace(cmd.Email))
	cmd.CreatedAt, cmd.ReleasedAt = cmd.CreatedAt.UTC(), cmd.ReleasedAt.UTC()
	if uc == nil || uc.repo == nil || cmd.ProjectID == 0 || cmd.ProductID == 0 || cmd.ResourceID == 0 ||
		cmd.Email == "" || cmd.CreatedAt.IsZero() || cmd.ReleasedAt.IsZero() || cmd.ReleasedAt.Before(cmd.CreatedAt) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	repo, ok := uc.repo.(DaumHistoryRepository)
	if !ok {
		return nil, fmt.Errorf("daum history repository is unavailable")
	}
	var result *domain.UnifiedAllocation
	err := uc.repo.WithTx(ctx, func(txCtx context.Context) error {
		locked, err := uc.repo.LockResourceRoot(txCtx, cmd.ResourceID, domain.AllocationTypeDaum)
		if err != nil {
			return err
		}
		if !locked {
			return domain.ErrInvalidAllocationRequest
		}
		resource, err := repo.LockDaumHistoricalResource(txCtx, cmd.ResourceID, cmd.Email)
		if err != nil {
			return err
		}
		if resource == nil || resource.OwnerUserID == 0 || !strings.EqualFold(resource.Email, cmd.Email) {
			return domain.ErrInvalidAllocationRequest
		}
		orderNo := historicalDaumAllocationOrderNo(cmd)
		existing, err := uc.repo.FindExistingAllocation(txCtx, orderNo)
		if err != nil {
			return err
		}
		if existing == nil {
			_, suffix, _ := strings.Cut(cmd.Email, "@")
			legacy, err := uc.repo.FindExistingAllocation(txCtx, fmt.Sprintf("HIST-DAUM-%d-%d-%s", cmd.ResourceID, cmd.ProjectID, suffix))
			if err != nil {
				return err
			}
			// A corrected identity keeps old facts but must not reuse their suffix-only key.
			if legacy != nil && strings.EqualFold(legacy.Email, cmd.Email) {
				existing = legacy
			}
		}
		if existing != nil {
			if existing.Type != domain.AllocationTypeDaum || existing.ResourceID != cmd.ResourceID ||
				existing.ProjectID != cmd.ProjectID || existing.ProductID != cmd.ProductID || existing.Mailbox != resource.Mailbox ||
				existing.Status != domain.AllocationStatusReleased || !strings.EqualFold(existing.Email, cmd.Email) {
				return domain.ErrAllocationConflict
			}
			result = existing
			return nil
		}
		matched, err := repo.HasDaumProjectHistory(txCtx, cmd.ResourceID, cmd.ProjectID, cmd.Email)
		if err != nil || matched {
			return err
		}
		if err := uc.repo.CreateOrderGuard(txCtx, orderNo, domain.AllocationTypeDaum); err != nil {
			return err
		}
		releasedAt := cmd.ReleasedAt
		allocation := &domain.DaumAllocation{
			OrderNo: orderNo, ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
			OwnerUserID: resource.OwnerUserID, SupplyScope: domain.SupplyScopePublic, Mailbox: resource.Mailbox, ExplicitAliasID: resource.ExplicitAliasID,
			ServiceMode: string(domain.ServiceModePurchase), Email: cmd.Email,
			Status: domain.AllocationStatusReleased, CostPointsSnapshot: "0.00",
			CreatedAt: cmd.CreatedAt, ReleasedAt: &releasedAt,
		}
		if err := repo.CreateDaumAllocation(txCtx, allocation); err != nil {
			return err
		}
		result = &domain.UnifiedAllocation{
			Type: domain.AllocationTypeDaum, ID: allocation.ID, OrderNo: orderNo,
			ProjectID: cmd.ProjectID, ProductID: cmd.ProductID, ResourceID: cmd.ResourceID,
			SupplyScope: domain.SupplyScopePublic, Mailbox: resource.Mailbox, Email: cmd.Email,
			Status: domain.AllocationStatusReleased, CreatedAt: allocation.CreatedAt, ReleasedAt: allocation.ReleasedAt, Created: true,
		}
		return nil
	})
	return result, err
}

func historicalDaumAllocationOrderNo(cmd HistoricalDaumAllocationCommand) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", cmd.ResourceID, cmd.ProjectID, strings.ToLower(strings.TrimSpace(cmd.Email)))))
	return "HIST-DAUM-" + hex.EncodeToString(sum[:20])
}
