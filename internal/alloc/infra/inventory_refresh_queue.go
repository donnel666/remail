package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeInventoryRefresh        = "alloc:inventory_refresh"
	TypePrivateInventoryRefresh = "alloc:private_inventory_refresh"
	inventoryRefreshTaskTimeout = 10 * time.Minute
)

type PrivateInventoryRefreshPayload struct {
	ProjectID    uint `json:"projectId"`
	ViewerUserID uint `json:"viewerUserId"`
}

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

func (q *InventoryRefreshQueue) EnqueuePrivateInventoryRefresh(ctx context.Context, projectID, viewerUserID uint) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("inventory refresh queue is unavailable")
	}
	if projectID == 0 || viewerUserID == 0 {
		return fmt.Errorf("private inventory refresh requires a project and viewer")
	}
	payload, err := json.Marshal(PrivateInventoryRefreshPayload{ProjectID: projectID, ViewerUserID: viewerUserID})
	if err != nil {
		return err
	}
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(TypePrivateInventoryRefresh, payload),
		asynq.Queue(platform.QueueBackgroundInventory),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(2*time.Minute),
		asynq.Retention(0),
		asynq.Unique(inventoryRefreshTaskTimeout),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
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
