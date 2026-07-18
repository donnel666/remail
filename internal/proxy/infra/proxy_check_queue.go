package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/platform"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/hibiken/asynq"
)

const (
	TypeProxyCheck           = "proxy:check"
	TypeProxyCheckDispatcher = "proxy:check_dispatcher"

	proxyQueueName                  = platform.QueueDefault
	proxyCheckTaskTimeout           = 90 * time.Second
	proxyCheckTaskUniqueTTL         = 15 * time.Minute
	proxyCheckDispatcherTaskTimeout = 30 * time.Second
)

type ProxyCheckQueue struct {
	client *asynq.Client
}

func NewProxyCheckQueue(client *asynq.Client) *ProxyCheckQueue {
	return &ProxyCheckQueue{client: client}
}

func (q *ProxyCheckQueue) EnqueueProxyCheck(ctx context.Context, task proxyapp.ProxyCheckTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("proxy check queue is unavailable")
	}
	if task.ProxyID == 0 || task.CheckGeneration == 0 {
		return false, fmt.Errorf("proxy check task identity is required")
	}
	payload, err := json.Marshal(struct {
		ProxyID         uint   `json:"proxyId"`
		CheckGeneration uint64 `json:"checkGeneration"`
	}{ProxyID: task.ProxyID, CheckGeneration: task.CheckGeneration})
	if err != nil {
		return false, fmt.Errorf("marshal proxy check task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeProxyCheck, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(proxyQueueName),
		asynq.Unique(proxyCheckTaskUniqueTTL),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetry),
		asynq.Timeout(proxyCheckTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue proxy check task: %w", err)
	}
	return true, nil
}

func (q *ProxyCheckQueue) EnqueueProxyCheckDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("proxy check queue is unavailable")
	}
	asynqTask := asynq.NewTask(TypeProxyCheckDispatcher, nil)
	uniqueTTL := proxyCheckDispatcherTaskTimeout
	if delay > 0 {
		uniqueTTL += delay
	}
	options := []asynq.Option{
		asynq.Queue(proxyQueueName),
		asynq.Unique(uniqueTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(proxyCheckDispatcherTaskTimeout),
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
		return fmt.Errorf("enqueue proxy check dispatcher task: %w", err)
	}
	return nil
}
