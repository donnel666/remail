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

const protoBucketCount = 2048

// SetProtoProtocolReady is wired by the composition root only after installing
// a real Proto mail adapter. Resource status alone cannot enable fulfillment.
func (uc *UseCase) SetProtoProtocolReady(ready bool) {
	if uc != nil {
		uc.protoProtocolReady.Store(ready)
	}
}

func (uc *UseCase) ProtoProtocolReady() bool {
	return uc != nil && uc.protoProtocolReady.Load()
}

func (uc *UseCase) findAllocationForFulfillment(ctx context.Context, orderNo string) (*domain.UnifiedAllocation, error) {
	allocation, err := uc.repo.FindExistingAllocation(ctx, orderNo)
	if err == nil && allocation != nil && allocation.Type == domain.AllocationTypeProto {
		ready, readyErr := uc.ProtoAllocationReady(ctx, orderNo, allocation.ID)
		if readyErr != nil {
			return nil, readyErr
		}
		if !ready {
			return nil, domain.ErrInsufficientInventory
		}
	}
	return allocation, err
}

// An unresolved paid order may still allocate. An existing allocation must have
// a currently normal resource and the session for its current credentials.
func (uc *UseCase) ProtoAllocationReady(ctx context.Context, orderNo string, allocationID uint) (bool, error) {
	if !uc.ProtoProtocolReady() {
		return false, nil
	}
	repo, ok := uc.repo.(interface {
		ProtoAllocationReady(context.Context, string, uint) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("proto allocation readiness repository is unavailable")
	}
	return repo.ProtoAllocationReady(ctx, orderNo, allocationID)
}

func (uc *UseCase) protoInventoryStats(stats *InventoryStats) *InventoryStats {
	if stats == nil || uc.protoProtocolReady.Load() {
		return stats
	}
	result := *stats
	result.TotalAvailable = max(0, result.TotalAvailable-result.Proto.TotalAvailable)
	result.Proto.MainAvailable, result.Proto.PublicAvailable, result.Proto.TotalAvailable = 0, 0, 0
	return &result
}

func (uc *UseCase) protoInventoryTotals(totals *ProjectProductInventoryTotals) *ProjectProductInventoryTotals {
	if totals == nil || uc.protoProtocolReady.Load() {
		return totals
	}
	hasProto := false
	for _, item := range totals.Items {
		hasProto = hasProto || item.ProductType == coredomain.ProductTypeProto
	}
	if !hasProto {
		return totals
	}
	result := cloneProductInventoryTotals(totals)
	for i := range result.Items {
		item := &result.Items[i]
		if item.ProductType != coredomain.ProductTypeProto {
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

func (uc *UseCase) allocateProto(ctx context.Context, cmd AllocateCommand, config ProductAllocationConfig) (*domain.UnifiedAllocation, error) {
	if cmd.EmailSuffix != "" || !domain.IsValidServiceMode(cmd.ServiceMode) {
		return nil, domain.ErrInvalidAllocationRequest
	}
	if !uc.protoProtocolReady.Load() {
		return nil, domain.ErrInsufficientInventory
	}
	repo, ok := uc.repo.(ProtoRepository)
	if !ok {
		return nil, fmt.Errorf("proto allocation repository is unavailable")
	}
	enabled, supplierPrice := config.CodeEnabled, config.CodeSupplierPrice
	if cmd.ServiceMode == domain.ServiceModePurchase {
		enabled, supplierPrice = config.PurchaseEnabled, config.PurchaseSupplierPrice
	}
	if !enabled {
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
	resourceBusy := false
	buckets := bucketProbeSequence(cmd.OrderNo, config.ProjectID, "proto", protoBucketCount)
	for probe := 0; probe <= len(buckets); probe++ {
		var bucket *uint16
		limit := globalCandidateWindowValue()
		if probe < len(buckets) {
			bucket, limit = &buckets[probe], candidateWindowSizeValue()
		} else {
			platform.RecordAllocationBucketFallback(string(domain.AllocationTypeProto), "probes_exhausted")
		}
		candidates, err := repo.ListProtoSourceCandidates(ctx, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope, bucket, limit)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			platform.AddAllocationCandidateAttempts(string(domain.AllocationTypeProto), 1)
			lockRoot := uc.repo.LockResourceRoot
			if cmd.lockResourceRoot != nil {
				lockRoot = cmd.lockResourceRoot
			}
			lockedRoot, err := lockRoot(ctx, candidate.ResourceID, domain.AllocationTypeProto)
			if err != nil {
				return nil, err
			}
			if !lockedRoot {
				resourceBusy = true
				continue
			}
			locked, err := repo.LockProtoCandidate(ctx, candidate.ResourceID, config.ProjectID, cmd.BuyerUserID, cmd.SupplyScope)
			if err != nil {
				return nil, err
			}
			if locked == nil {
				platform.RecordAllocationCandidateRecheckMiss(string(domain.AllocationTypeProto))
				continue
			}
			if cmd.ensureOrderGuard == nil {
				return nil, domain.ErrAllocationTxRequired
			}
			allocation := &domain.ProtoAllocation{
				OrderNo: cmd.OrderNo, ProjectID: config.ProjectID, ProductID: config.ProductID,
				ResourceID: locked.ResourceID, OwnerUserID: locked.OwnerUserID, SupplyScope: cmd.SupplyScope, Mailbox: "main",
				ServiceMode: string(cmd.ServiceMode), Email: strings.ToLower(strings.TrimSpace(locked.Email)),
				Status: domain.AllocationStatusAllocated, CostPointsSnapshot: costSnapshot, CreatedAt: now,
			}
			if allocation.Email == "" {
				return nil, domain.ErrInvalidAllocationRequest
			}
			if err := cmd.ensureOrderGuard(ctx, domain.AllocationTypeProto); err != nil {
				return nil, err
			}
			if err := repo.CreateProtoAllocation(ctx, allocation); err != nil {
				return nil, err
			}
			if err := repo.TouchProtoAllocated(ctx, allocation.ResourceID, now); err != nil {
				return nil, err
			}
			return &domain.UnifiedAllocation{
				Type: domain.AllocationTypeProto, ID: allocation.ID, OrderNo: allocation.OrderNo,
				ProjectID: allocation.ProjectID, ProductID: allocation.ProductID, ResourceID: allocation.ResourceID,
				SupplyScope: allocation.SupplyScope, Mailbox: allocation.Mailbox, Email: allocation.Email,
				Status: allocation.Status, CreatedAt: allocation.CreatedAt,
			}, nil
		}
	}
	if resourceBusy {
		return nil, errResourceTypeBusy
	}
	return nil, domain.ErrInsufficientInventory
}
