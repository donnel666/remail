// Package successranking owns the successful-order ranking query shared by
// the dashboard and daily reward settlement.
package successranking

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type Row struct {
	UserID        uint      `gorm:"column:user_id"`
	Nickname      string    `gorm:"column:nickname"`
	Email         string    `gorm:"column:email"`
	Score         int       `gorm:"column:score"`
	LastSuccessAt time.Time `gorm:"column:last_success_at"`
}

// Query ranks non-refunded successful orders by their success occurrence time.
// Bounds are half-open and nil means unbounded.
func Query(ctx context.Context, db *gorm.DB, from, to *time.Time, limit int) ([]Row, error) {
	codeIndex, purchaseIndex := "", ""
	if from != nil || to != nil {
		codeIndex = " FORCE INDEX (idx_mailmatch_delivery_heads_received)"
		purchaseIndex = " FORCE INDEX (idx_orders_activated)"
	}
	codeSQL := `
SELECT o.user_id, h.message_received_at AS success_at
FROM mailmatch_order_delivery_heads AS h` + codeIndex + `
JOIN orders AS o ON o.id = h.order_id
WHERE o.service_mode = 'code'
  AND o.order_no NOT LIKE 'HIST-%'
  AND o.status NOT IN ('refunded', 'failed')`
	purchaseSQL := `
SELECT o.user_id, o.activated_at AS success_at
FROM orders AS o` + purchaseIndex + `
WHERE o.service_mode = 'purchase'
  AND o.activated_at IS NOT NULL
  AND o.order_no NOT LIKE 'HIST-%'
  AND o.status NOT IN ('refunded', 'failed')`
	codeArgs, purchaseArgs := make([]any, 0, 2), make([]any, 0, 2)
	if from != nil {
		codeSQL += "\n  AND h.message_received_at >= ?"
		purchaseSQL += "\n  AND o.activated_at >= ?"
		codeArgs, purchaseArgs = append(codeArgs, from.UTC()), append(purchaseArgs, from.UTC())
	}
	if to != nil {
		codeSQL += "\n  AND h.message_received_at < ?"
		purchaseSQL += "\n  AND o.activated_at < ?"
		codeArgs, purchaseArgs = append(codeArgs, to.UTC()), append(purchaseArgs, to.UTC())
	}
	query := `
SELECT successes.user_id,
       COALESCE(u.nickname, '') AS nickname,
       COALESCE(u.email, '') AS email,
       COUNT(*) AS score,
       MAX(successes.success_at) AS last_success_at
FROM (` + codeSQL + `
UNION ALL` + purchaseSQL + `
) AS successes
JOIN users AS u ON u.id = successes.user_id
GROUP BY successes.user_id, u.nickname, u.email
ORDER BY score DESC, last_success_at ASC, successes.user_id ASC`
	args := append(codeArgs, purchaseArgs...)
	if limit > 0 {
		query += "\nLIMIT ?"
		args = append(args, limit)
	}
	var rows []Row
	err := db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error
	return rows, err
}
