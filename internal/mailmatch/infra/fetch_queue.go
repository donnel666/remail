package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/hibiken/asynq"
)

const (
	TypeMailmatchFetch           = "mailmatch:fetch"
	TypeMailmatchFetchDispatcher = "mailmatch:fetch_dispatcher"

	mailmatchQueueName           = "mailfetch"
	mailmatchFetchTaskMaxRetry   = 0
	mailmatchFetchTaskTimeout    = 60 * time.Second
	mailmatchDispatchTaskTimeout = 30 * time.Second
)

type FetchQueue struct {
	client *asynq.Client
}

func NewFetchQueue(client *asynq.Client) *FetchQueue {
	return &FetchQueue{client: client}
}

func (q *FetchQueue) EnqueueFetch(ctx context.Context, task app.FetchTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("mailmatch fetch queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal mailmatch fetch task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeMailmatchFetch, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(mailmatchQueueName),
		asynq.TaskID(fmt.Sprintf("%s:%d", TypeMailmatchFetch, task.JobID)),
		asynq.MaxRetry(mailmatchFetchTaskMaxRetry),
		asynq.Timeout(mailmatchFetchTaskTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue mailmatch fetch task: %w", err)
	}
	return nil
}

func (q *FetchQueue) EnqueueFetchDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("mailmatch fetch dispatcher queue is unavailable")
	}
	task := asynq.NewTask(TypeMailmatchFetchDispatcher, nil)
	options := []asynq.Option{
		asynq.Queue(mailmatchQueueName),
		asynq.TaskID(TypeMailmatchFetchDispatcher),
		asynq.MaxRetry(0),
		asynq.Timeout(mailmatchDispatchTaskTimeout),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, task, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue mailmatch fetch dispatcher task: %w", err)
	}
	return nil
}
