package infra

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"gorm.io/gorm"
)

// ListOrderMessagePage keeps auxiliary paging within the same persisted ownership as pickup.
func (r *Repo) ListOrderMessagePage(ctx context.Context, scope app.OrderScope, query app.AdminMessageListQuery) ([]app.AdminMessageSummary, int64, bool, error) {
	_, maximum, searchLimit := app.AdminMessageLimits()
	if scope.OrderID == 0 || scope.OrderNo == "" || query.Offset < 0 || query.Limit < 1 || query.Limit > maximum ||
		(query.BeforeReceivedAt == nil) != (query.BeforeID == 0) || (query.BeforeReceivedAt != nil && query.Offset != 0) || utf8.RuneCountInString(query.Search) > searchLimit {
		return nil, 0, false, domain.ErrInvalidRequest
	}
	start, end := app.OrderReadWindow(scope, time.Now().UTC())
	owned := r.dbFor(ctx).Raw(fmt.Sprintf(`
SELECT %s
FROM mailmatch_message_projections AS mp
JOIN mailmatch_messages AS m ON m.id = mp.message_id
WHERE mp.matched_order_id = ? AND mp.message_received_at >= ? AND mp.message_received_at <= ?
UNION ALL
SELECT %s
FROM mailmatch_messages AS m
LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id
WHERE mp.message_id IS NULL AND m.matched_order_id = ? AND m.received_at >= ? AND m.received_at <= ?`,
		projectionOwnedMessageColumns(false), legacyMessageColumns(false)), scope.OrderID, start, end, scope.OrderID, start, end)
	base := r.dbFor(ctx).Table("(?) AS owned", owned)
	if search := strings.TrimSpace(query.Search); search != "" {
		like := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(search) + "%"
		base = base.Where(`owned.recipient LIKE ? ESCAPE '!' OR owned.sender LIKE ? ESCAPE '!' OR owned.subject LIKE ? ESCAPE '!' OR owned.body_preview LIKE ? ESCAPE '!' OR owned.verification_code LIKE ? ESCAPE '!'`, like, like, like, like, like)
	}
	total := int64(-1)
	if !query.SkipTotal {
		if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return nil, 0, false, fmt.Errorf("count order auxiliary messages: %w", err)
		}
	}
	page := base.Session(&gorm.Session{})
	if query.BeforeReceivedAt != nil {
		page = page.Where("owned.received_at < ? OR (owned.received_at = ? AND owned.id < ?)", query.BeforeReceivedAt.UTC(), query.BeforeReceivedAt.UTC(), query.BeforeID)
	} else {
		page = page.Offset(query.Offset)
	}
	var rows []adminMessageRow
	if err := page.Select("owned.id, 'main' AS mailbox, owned.recipient, owned.sender, owned.subject, owned.body_preview AS preview, owned.status, owned.verification_code, ? AS order_no, owned.received_at", scope.OrderNo).
		Order("owned.received_at DESC, owned.id DESC").Limit(query.Limit + 1).Scan(&rows).Error; err != nil {
		return nil, 0, false, fmt.Errorf("list order auxiliary messages: %w", err)
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]app.AdminMessageSummary, len(rows))
	for i, row := range rows {
		items[i] = adminMessageSummaryFromRow(row)
	}
	return items, total, hasMore, nil
}
