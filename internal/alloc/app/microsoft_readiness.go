package app

import (
	"context"
	"fmt"
)

func (uc *UseCase) MicrosoftAllocationReady(ctx context.Context, orderNo string, allocationID uint) (bool, error) {
	repo, ok := uc.repo.(interface {
		MicrosoftAllocationReady(context.Context, string, uint) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("microsoft allocation readiness repository is unavailable")
	}
	return repo.MicrosoftAllocationReady(ctx, orderNo, allocationID)
}
