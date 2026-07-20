package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeCandidateRefresh           = "alloc:candidate_refresh"
	TypeCandidateRefreshDispatcher = "alloc:candidate_refresh_dispatcher"
	TypeInventoryRefresh           = "alloc:inventory_refresh"

	allocationQueueName                   = platform.QueueDefault
	candidateRefreshTaskTimeout           = 5 * time.Minute
	candidateRefreshDispatcherTaskTimeout = 30 * time.Second
	inventoryRefreshTaskTimeout           = 10 * time.Minute
)

type CandidateRefreshQueue struct {
	client *asynq.Client
}

func (q *CandidateRefreshQueue) EnqueueInventoryRefresh(ctx context.Context) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("inventory refresh queue is unavailable")
	}
	_, err := q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeInventoryRefresh, nil),
		asynq.Queue(platform.QueueBackgroundInventory),
		asynq.Unique(inventoryRefreshTaskTimeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetry),
		asynq.Timeout(inventoryRefreshTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue inventory refresh task: %w", err)
	}
	return nil
}

func NewCandidateRefreshQueue(client *asynq.Client) *CandidateRefreshQueue {
	return &CandidateRefreshQueue{client: client}
}

func (q *CandidateRefreshQueue) EnqueueCandidateRefresh(ctx context.Context, task allocapp.CandidateRefreshTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("candidate refresh queue is unavailable")
	}
	if task.ProjectID == 0 || task.Generation == 0 {
		return false, fmt.Errorf("candidate refresh task identity is missing")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal candidate refresh task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeCandidateRefresh, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(allocationQueueName),
		asynq.Unique(candidateRefreshTaskTimeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetry),
		asynq.Timeout(candidateRefreshTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue candidate refresh task: %w", err)
	}
	return true, nil
}

func (q *CandidateRefreshQueue) EnqueueCandidateRefreshDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("candidate refresh dispatcher queue is unavailable")
	}
	asynqTask := asynq.NewTask(TypeCandidateRefreshDispatcher, nil)
	uniqueTTL := candidateRefreshDispatcherTaskTimeout
	if delay > 0 {
		uniqueTTL += delay
	}
	options := []asynq.Option{
		asynq.Queue(allocationQueueName),
		asynq.Unique(uniqueTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(candidateRefreshDispatcherTaskTimeout),
		asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynqTask, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue candidate refresh dispatcher task: %w", err)
	}
	return nil
}
