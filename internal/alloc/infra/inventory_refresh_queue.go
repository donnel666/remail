package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeInventoryRefresh        = "alloc:inventory_refresh"
	inventoryRefreshTaskTimeout = 10 * time.Minute
)

type InventoryRefreshQueue struct {
	client *asynq.Client
}

func (q *InventoryRefreshQueue) EnqueueInventoryRefresh(ctx context.Context) error {
	return q.enqueueInventoryRefresh(ctx, true)
}

func (q *InventoryRefreshQueue) EnqueueInventoryRefreshContinuation(ctx context.Context) error {
	// The current task still owns the normal uniqueness lock. Scheduled-cache claims
	// are atomic, so a rare duplicate continuation only finds a disjoint batch.
	return q.enqueueInventoryRefresh(ctx, false)
}

func (q *InventoryRefreshQueue) enqueueInventoryRefresh(ctx context.Context, unique bool) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("inventory refresh queue is unavailable")
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundInventory),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(inventoryRefreshTaskTimeout),
		asynq.Retention(0),
	}
	if unique {
		options = append(options, asynq.Unique(inventoryRefreshTaskTimeout))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeInventoryRefresh, nil), options...)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue inventory refresh task: %w", err)
	}
	return nil
}

func NewInventoryRefreshQueue(client *asynq.Client) *InventoryRefreshQueue {
	return &InventoryRefreshQueue{client: client}
}
