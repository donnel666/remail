package app

import (
	"context"
	"fmt"
)

func (uc *UseCase) AllocationSupplierUserID(ctx context.Context, orderNo string) (uint, error) {
	repo, ok := uc.repo.(interface {
		AllocationSupplierUserID(context.Context, string) (uint, error)
	})
	if !ok {
		return 0, fmt.Errorf("allocation supplier snapshots unavailable")
	}
	return repo.AllocationSupplierUserID(ctx, orderNo)
}
