package api

import (
	"context"
	"errors"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	coreapp "github.com/donnel666/remail/internal/core/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
)

// ResourceAllocationGuardAdapter publishes the Alloc-owned active allocation
// invariant through Core's consumer-defined port. It deliberately translates
// errors instead of exposing Alloc domain types across the boundary.
type ResourceAllocationGuardAdapter struct {
	alloc *allocapp.UseCase
}

func NewResourceAllocationGuardAdapter(alloc *allocapp.UseCase) *ResourceAllocationGuardAdapter {
	return &ResourceAllocationGuardAdapter{alloc: alloc}
}

func (a *ResourceAllocationGuardAdapter) AssertNoActiveAllocations(ctx context.Context, resourceIDs []uint) error {
	if a == nil || a.alloc == nil {
		return coredomain.ErrResourceDependency
	}
	if err := a.alloc.AssertNoActiveAllocations(ctx, resourceIDs); err != nil {
		if errors.Is(err, allocdomain.ErrActiveAllocation) {
			return coredomain.ErrResourceHasAllocation
		}
		return err
	}
	return nil
}

var _ coreapp.ResourceAllocationGuardPort = (*ResourceAllocationGuardAdapter)(nil)
