package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	moneyfmt "github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
)

// SetDaumProtocolReady is wired by the composition root only after installing
// a real Daum mail adapter. Resource status alone cannot enable fulfillment.
func (uc *UseCase) SetDaumProtocolReady(ready bool) {
	if uc != nil {
		uc.daumProtocolReady.Store(ready)
	}
}

func (uc *UseCase) DaumProtocolReady() bool {
	return uc != nil && uc.daumProtocolReady.Load()
}

// An unresolved paid order may still allocate. An existing allocation must have
// a currently normal resource and the original deliverable address.
func (uc *UseCase) DaumAllocationReady(ctx context.Context, orderNo string, allocationID uint) (bool, error) {
	if !uc.DaumProtocolReady() {
		return false, nil
	}
	repo, ok := uc.repo.(interface {
		DaumAllocationReady(context.Context, string, uint) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("daum allocation readiness repository is unavailable")
	}
	return repo.DaumAllocationReady(ctx, orderNo, allocationID)
}

func (uc *UseCase) daumInventoryStats(stats *InventoryStats) *InventoryStats {
	if stats == nil || uc.daumProtocolReady.Load() {
		return stats
	}
	result := *stats
	result.TotalAvailable = max(0, result.TotalAvailable-result.Daum.TotalAvailable)
	result.Daum.MainAvailable, result.Daum.ExplicitAliasAvailable, result.Daum.PublicAvailable, result.Daum.TotalAvailable = 0, 0, 0, 0
	return &result
}

func (uc *UseCase) daumInventoryTotals(totals *ProjectProductInventoryTotals) *ProjectProductInventoryTotals {
	if totals == nil || uc.daumProtocolReady.Load() {
		return totals
	}
	hasDaum := false
	for _, item := range totals.Items {
		hasDaum = hasDaum || item.ProductType == coredomain.ProductTypeDaum
	}
	if !hasDaum {
		return totals
	}
	result := cloneProductInventoryTotals(totals)
	for i := range result.Items {
		item := &result.Items[i]
		if item.ProductType != coredomain.ProductTypeDaum {
			continue
		}
		result.TotalAvailable = max(0, result.TotalAvailable-item.TotalAvailable)
		item.TotalAvailable, item.PublicAvailable = 0, 0
		zero := int64(0)
		item.CodeAvailable, item.CodePublicAvailable = &zero, &zero
		item.PurchaseAvailable, item.PurchasePublicAvailable = &zero, &zero
		for j := range item.Suffixes {
			item.Suffixes[j].TotalAvailable, item.Suffixes[j].PublicAvailable = 0, 0
		}
	}
	return result
}

func (uc *UseCase) allocateDaum(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig, probes map[domain.SupplyScope]*bucketProbeWindow) (*domain.UnifiedAllocation, error) {
	if (cmd.EmailSuffix != "" && !coredomain.IsDaumEmailSuffix(cmd.EmailSuffix)) || !domain.IsValidServiceMode(cmd.ServiceMode) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if !uc.daumProtocolReady.Load() {
		return nil, domain.ErrInsufficientInventory
	}
	repo, ok := uc.repo.(DaumRepository)
	if !ok {
		return nil, fmt.Errorf("daum allocation repository is unavailable")
	}
	enabled, supplierPrice := config.CodeEnabled, config.CodeSupplierPrice
	if cmd.ServiceMode == domain.ServiceModePurchase {
		enabled, supplierPrice = config.PurchaseEnabled, config.PurchaseSupplierPrice
	}
	if !enabled && !cmd.FulfillExistingOrder {
		return nil, domain.ErrProjectNotAllocatable
	}
	cost, err := moneyfmt.Parse(supplierPrice)
	if err != nil || cost.IsNegative() {
		return nil, domain.ErrProjectNotAllocatable
	}
	costSnapshot := moneyfmt.Format(cost)
	if cmd.SupplyScope == domain.SupplyScopeOwned {
		costSnapshot = "0.00"
	}
	now := time.Now().UTC()
	window := probes[cmd.SupplyScope]
	if window == nil {
		window = newBucketProbeWindow(cmd.OrderNo, config.ProjectID, "main", DaumBucketCount, int(DaumBucketCount))
		probes[cmd.SupplyScope] = window
	}
	for len(window.batches) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		buckets := window.batches[0]
		window.batches = window.batches[1:]
		limit := candidateWindowSizeValue()
		if len(buckets) > 1 {
			limit = globalCandidateWindowValue()
		}
		candidates, err := repo.ListDaumSourceCandidates(ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, buckets, limit, cmd.EmailSuffix)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			platform.AddAllocationCandidateAttempts(string(domain.AllocationTypeDaum), 1)
			lockRoot := uc.repo.LockResourceRoot
			if cmd.lockResourceRoot != nil {
				lockRoot = cmd.lockResourceRoot
			}
			lockedRoot, err := lockRoot(ctx, candidate.ResourceID, domain.AllocationTypeDaum)
			if err != nil {
				return nil, err
			}
			if !lockedRoot {
				window.busy = true
				continue
			}
			locked, err := repo.LockDaumCandidate(ctx, candidate, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, cmd.EmailSuffix)
			if err != nil {
				return nil, err
			}
			if locked == nil {
				platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeDaum))
				continue
			}
			if cmd.ensureOrderGuard == nil {
				return nil, domain.ErrAllocationTxRequired
			}
			allocation := &domain.DaumAllocation{
				OrderNo: cmd.OrderNo, ProjectID: config.ProjectID, ProductID: config.ProductID,
				ResourceID: locked.ResourceID, OwnerUserID: locked.OwnerUserID, SupplyScope: cmd.SupplyScope, Mailbox: locked.Mailbox, ExplicitAliasID: locked.ExplicitAliasID,
				ServiceMode: string(cmd.ServiceMode), Email: strings.ToLower(strings.TrimSpace(locked.Email)),
				Status: domain.AllocationStatusAllocated, CostPointsSnapshot: costSnapshot, CreatedAt: now,
			}
			if allocation.Email == "" {
				return nil, domain.ErrInvalidAllocationRequest
			}
			if err := cmd.ensureOrderGuard(ctx, domain.AllocationTypeDaum); err != nil {
				return nil, err
			}
			if err := repo.CreateDaumAllocation(ctx, allocation); err != nil {
				return nil, err
			}
			if err := repo.TouchDaumAllocated(ctx, allocation.ResourceID, now); err != nil {
				return nil, err
			}
			return &domain.UnifiedAllocation{
				Type: domain.AllocationTypeDaum, ID: allocation.ID, OrderNo: allocation.OrderNo,
				ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
				SupplyScope: allocation.SupplyScope, Mailbox: allocation.Mailbox, Email: allocation.Email,
				Status: allocation.Status, CreatedAt: allocation.CreatedAt,
			}, nil
		}
	}
	if window.busy {
		return nil, errBucketProbeBudgetExhausted
	}
	return nil, domain.ErrInsufficientInventory
}
