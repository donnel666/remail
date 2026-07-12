package infra

import (
	"context"
	"encoding/json"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/hibiken/asynq"
)

const (
	TypeAdminResourceBulk           = "core:admin_resource_bulk"
	TypeAdminResourceBulkDispatcher = "core:admin_resource_bulk_dispatcher"
	AdminResourceBulkQueueName      = "resource"
)

type AdminResourceBulkQueue struct {
	client *asynq.Client
}

func NewAdminResourceBulkQueue(client *asynq.Client) *AdminResourceBulkQueue {
	return &AdminResourceBulkQueue{client: client}
}

func (q *AdminResourceBulkQueue) EnqueueAdminResourceBulk(ctx context.Context, task coreapp.AdminResourceBulkTask) error {
	if q == nil || q.client == nil {
		return nil
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(TypeAdminResourceBulk, payload),
		asynq.Queue(AdminResourceBulkQueueName),
		asynq.MaxRetry(0),
		asynq.TaskID(adminResourceBulkTaskID(task)),
	)
	if err == asynq.ErrTaskIDConflict {
		return nil
	}
	return err
}

func (q *AdminResourceBulkQueue) EnqueueAdminResourceBulkDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return nil
	}
	options := []asynq.Option{
		asynq.Queue(AdminResourceBulkQueueName),
		asynq.MaxRetry(0),
		asynq.TaskID("core-admin-resource-bulk-dispatcher"),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeAdminResourceBulkDispatcher, nil), options...)
	if err == asynq.ErrTaskIDConflict {
		return nil
	}
	return err
}

func adminResourceBulkTaskID(task coreapp.AdminResourceBulkTask) string {
	return "core-admin-resource-bulk-" + task.DispatchToken
}

var _ coreapp.AdminResourceBulkQueue = (*AdminResourceBulkQueue)(nil)
