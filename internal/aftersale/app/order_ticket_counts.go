package app

import (
	"context"
	"fmt"
)

type orderTicketCountRepository interface {
	CountOrderTicketsByUsers(ctx context.Context, userIDs []uint) (map[uint]int64, error)
}

// CountOrderTicketsByUsers counts applications regardless of their resolution.
func (uc *UseCase) CountOrderTicketsByUsers(ctx context.Context, userIDs []uint) (map[uint]int64, error) {
	repo, ok := uc.repo.(orderTicketCountRepository)
	if !ok {
		return nil, fmt.Errorf("order ticket counts unavailable")
	}
	return repo.CountOrderTicketsByUsers(ctx, userIDs)
}
