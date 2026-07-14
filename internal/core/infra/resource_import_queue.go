package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeMicrosoftImport = "core:microsoft_import"

	importQueueName    = platform.QueueDefault
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
		asynq.TaskID(importTaskID(task)),
		asynq.MaxRetry(importMaxRetry(task)),
		asynq.Timeout(importTaskTimeout),
	)
	if err == asynq.ErrTaskIDConflict {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue microsoft import task: %w", err)
	}
	return nil
}

func importTaskID(task coreapp.MicrosoftImportTask) string {
	if task.DispatchToken != "" {
		return fmt.Sprintf("%s:%d:%s", TypeMicrosoftImport, task.ImportID, task.DispatchToken)
	}
	return fmt.Sprintf("%s:%d", TypeMicrosoftImport, task.ImportID)
}

func importMaxRetry(task coreapp.MicrosoftImportTask) int {
	// Administrator imports retry from their durable database fact with a new
	// dispatch token. Replaying this Asynq payload would reuse a consumed token.
	if task.DispatchToken != "" {
		return 0
	}
	return importTaskMaxRetry
}
