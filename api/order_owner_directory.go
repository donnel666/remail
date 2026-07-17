package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
)

// orderOwnerDirectory adapts IAM's owner-summary port to trade's OwnerLookupPort
// so the administrator site-wide order list can show each order's buyer. It
// reuses the same IAM adapter that enriches admin resource owners and lives in
// the composition root so the trade context need not depend on core.
type orderOwnerDirectory struct {
	owners coreapp.OwnerQueryPort
}

func (d orderOwnerDirectory) GetByIDs(ctx context.Context, ids []uint) (map[uint]tradeapp.OrderOwnerSummary, error) {
	if d.owners == nil || len(ids) == 0 {
		return map[uint]tradeapp.OrderOwnerSummary{}, nil
	}
	summaries, err := d.owners.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]tradeapp.OrderOwnerSummary, len(summaries))
	for id, summary := range summaries {
		out[id] = tradeapp.OrderOwnerSummary{
			ID:        summary.ID,
			Email:     summary.Email,
			Nickname:  summary.Nickname,
			GroupName: summary.GroupName,
			Role:      summary.Role,
			Enabled:   summary.Enabled,
		}
	}
	return out, nil
}
