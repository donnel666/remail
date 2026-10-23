package infra

import (
	"context"
	"fmt"

	"github.com/donnel666/remail/internal/aftersale/domain"
)

func (r *Repo) CountOrderTicketsByUsers(ctx context.Context, userIDs []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(userIDs))
	if len(userIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		RequesterUserID uint
		TicketCount     int64
	}
	if err := r.dbFor(ctx).Model(&TicketModel{}).
		Select("requester_user_id, COUNT(*) AS ticket_count").
		Where("requester_user_id IN ? AND ticket_type = ?", userIDs, domain.TicketTypeOrder).
		Group("requester_user_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count order tickets by users: %w", err)
	}
	for _, row := range rows {
		counts[row.RequesterUserID] = row.TicketCount
	}
	return counts, nil
}
