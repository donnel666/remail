package api

import (
	"context"

	aftersaleapp "github.com/donnel666/remail/internal/aftersale/app"
	coreapp "github.com/donnel666/remail/internal/core/app"
)

// ticketRequesterDirectory adapts IAM's owner-summary port to the aftersale
// OwnerLookupPort so the ticket console can show each requester. It reuses the
// same IAM adapter that enriches admin resource owners and order buyers, and
// lives in the composition root so aftersale need not depend on core.
type ticketRequesterDirectory struct {
	owners coreapp.OwnerQueryPort
}

func (d ticketRequesterDirectory) GetByIDs(ctx context.Context, ids []uint) (map[uint]aftersaleapp.RequesterSummary, error) {
	if d.owners == nil || len(ids) == 0 {
		return map[uint]aftersaleapp.RequesterSummary{}, nil
	}
	summaries, err := d.owners.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]aftersaleapp.RequesterSummary, len(summaries))
	for id, summary := range summaries {
		out[id] = aftersaleapp.RequesterSummary{
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
