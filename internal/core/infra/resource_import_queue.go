package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/hibiken/asynq"
)

const (
	TypeMicrosoftImport = "core:microsoft_import"

	importQueueName    = "default"
	importTaskMaxRetry = 3
	importTaskTimeout  = 30 * time.Minute
)

// ResourceImportQueue enqueues resource import tasks in Asynq.
type ResourceImportQueue struct {
	client *asynq.Client
}

// NewResourceImportQueue creates an Asynq-backed resource import queue.
func NewResourceImportQueue(client *asynq.Client) *ResourceImportQueue {
	return &ResourceImportQueue{client: client}
}

func (q *ResourceImportQueue) EnqueueMicrosoftImport(ctx context.Context, task coreapp.MicrosoftImportTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal microsoft import task: %w", err)
	}

	asynqTask := asynq.NewTask(TypeMicrosoftImport, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(importQueueName),
		asynq.TaskID(fmt.Sprintf("%s:%d", TypeMicrosoftImport, task.ImportID)),
		asynq.MaxRetry(importTaskMaxRetry),
		asynq.Timeout(importTaskTimeout),
	)
	if err != nil {
		return fmt.Errorf("enqueue microsoft import task: %w", err)
	}
	return nil
}
