package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeMicrosoftImport = "core:microsoft_import"

	importQueueName       = platform.QueueDefault
	importTaskMaxRetry    = platform.BackgroundTaskMaxRetry
	importTaskTimeout     = 30 * time.Minute
	importActivationDelay = time.Second
)

// ResourceImportQueue enqueues resource import tasks in Asynq.
type ResourceImportQueue struct {
	client *asynq.Client
}

// NewResourceImportQueue creates an Asynq-backed resource import queue.
func NewResourceImportQueue(client *asynq.Client) *ResourceImportQueue {
	return &ResourceImportQueue{client: client}
}

func (q *ResourceImportQueue) EnqueueMicrosoftImport(ctx context.Context, task coreapp.MicrosoftImportTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("resource import queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal microsoft import task: %w", err)
	}

	asynqTask := asynq.NewTask(TypeMicrosoftImport, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(importQueueName),
		asynq.Unique(importTaskTimeout+importActivationDelay),
		asynq.MaxRetry(importTaskMaxRetry),
		asynq.Timeout(importTaskTimeout),
		asynq.ProcessIn(importActivationDelay),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue microsoft import task: %w", err)
	}
	return true, nil
}
