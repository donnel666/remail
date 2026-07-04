package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/hibiken/asynq"
)

const (
	TypeResourceValidation           = "core:resource_validation"
	TypeResourceValidationDispatcher = "core:resource_validation_dispatcher"

	validationQueueName             = "default"
	validationTaskMaxRetry          = 3
	validationTaskTimeout           = 3 * time.Minute
	validationDispatcherTaskTimeout = 30 * time.Second
)

type ResourceValidationQueue struct {
	client *asynq.Client
}

func NewResourceValidationQueue(client *asynq.Client) *ResourceValidationQueue {
	return &ResourceValidationQueue{client: client}
}

func (q *ResourceValidationQueue) EnqueueResourceValidation(ctx context.Context, task coreapp.ResourceValidationTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("resource validation queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal resource validation task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeResourceValidation, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(validationQueueName),
		asynq.MaxRetry(validationTaskMaxRetry),
		asynq.Timeout(validationTaskTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue resource validation task: %w", err)
	}
	return nil
}

func (q *ResourceValidationQueue) EnqueueResourceValidationDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("resource validation queue is unavailable")
	}
	asynqTask := asynq.NewTask(TypeResourceValidationDispatcher, nil)
	options := []asynq.Option{
		asynq.Queue(validationQueueName),
		asynq.TaskID(TypeResourceValidationDispatcher),
		asynq.MaxRetry(0),
		asynq.Timeout(validationDispatcherTaskTimeout),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynqTask, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue resource validation dispatcher task: %w", err)
	}
	return nil
}
