package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/hibiken/asynq"
)

const (
	TypeProxyCheck           = "proxy:check"
	TypeProxyCheckBatch      = "proxy:check_batch"
	TypeProxyCheckDispatcher = "proxy:check_dispatcher"

	proxyQueueName                  = "default"
	proxyCheckTaskTimeout           = 90 * time.Second
	proxyCheckBatchTaskTimeout      = 10 * time.Minute
	proxyCheckDispatcherTaskTimeout = 30 * time.Second
)

type ProxyCheckQueue struct {
	client *asynq.Client
}

func NewProxyCheckQueue(client *asynq.Client) *ProxyCheckQueue {
	return &ProxyCheckQueue{client: client}
}

func (q *ProxyCheckQueue) EnqueueProxyCheck(ctx context.Context, task proxyapp.ProxyCheckTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("proxy check queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal proxy check task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeProxyCheck, payload)
	options := []asynq.Option{
		asynq.Queue(proxyQueueName),
		asynq.MaxRetry(0),
		asynq.Timeout(proxyCheckTaskTimeout),
	}
	if task.JobID != 0 {
		options = append(options, asynq.TaskID(fmt.Sprintf("%s:%d", TypeProxyCheck, task.JobID)))
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		options...,
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue proxy check task: %w", err)
	}
	return nil
}

func (q *ProxyCheckQueue) EnqueueProxyCheckBatch(ctx context.Context, task proxyapp.ProxyCheckBatchTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("proxy check queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal proxy check batch task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeProxyCheckBatch, payload)
	options := []asynq.Option{
		asynq.Queue(proxyQueueName),
		asynq.MaxRetry(0),
		asynq.Timeout(proxyCheckBatchTaskTimeout),
	}
	if task.JobID != 0 {
		options = append(options, asynq.TaskID(fmt.Sprintf("%s:%d", TypeProxyCheckBatch, task.JobID)))
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		options...,
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue proxy check batch task: %w", err)
	}
	return nil
}

func (q *ProxyCheckQueue) EnqueueProxyCheckDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("proxy check queue is unavailable")
	}
	asynqTask := asynq.NewTask(TypeProxyCheckDispatcher, nil)
	options := []asynq.Option{
		asynq.Queue(proxyQueueName),
		asynq.MaxRetry(0),
		asynq.Timeout(proxyCheckDispatcherTaskTimeout),
		asynq.TaskID(TypeProxyCheckDispatcher),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynqTask, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue proxy check dispatcher task: %w", err)
	}
	return nil
}
