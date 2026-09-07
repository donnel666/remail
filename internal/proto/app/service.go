package app

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
	TaskImport                   = "proto:resource_import"
	TaskImportDispatcher         = "proto:resource_import_dispatcher"
	TaskValidate                 = "proto:validate"
	TaskValidationDispatcher     = "proto:validation_dispatcher"
	TaskHistory                  = "proto:history"
	TaskHistoryDispatcher        = "proto:history_dispatcher"
	TaskBulk                     = "proto:resource_bulk"
	TaskProjectHistory           = "proto:project_history"
	TaskProjectHistoryDispatcher = "proto:project_history_dispatcher"
	QueueProtoImport             = platform.QueueBackgroundProtoImport
	QueueProtoValidation         = platform.QueueBackgroundProtoValidation
	QueueProtoHistory            = platform.QueueBackgroundProtoHistory
)

type ImportTaskPayload struct {
	ImportID   uint64 `json:"importId"`
	Generation uint64 `json:"generation"`
}
type ValidationTaskPayload struct {
	ResourceID           uint   `json:"resourceId"`
	OwnerUserID          uint   `json:"ownerUserId"`
	ValidationGeneration uint64 `json:"validationGeneration"`
	CredentialRevision   uint64 `json:"credentialRevision"`
	MaintenanceRunID     uint64 `json:"maintenanceRunId,omitempty"`
	RequestID            string `json:"requestId,omitempty"`
}
type HistoryTaskPayload struct {
	ResourceID           uint   `json:"resourceId"`
	OwnerUserID          uint   `json:"ownerUserId"`
	CredentialRevision   uint64 `json:"credentialRevision"`
	ValidationGeneration uint64 `json:"validationGeneration"`
	MaintenanceRunID     uint64 `json:"maintenanceRunId,omitempty"`
	RequestID            string `json:"requestId,omitempty"`
}

// Queue is the narrow dependency needed by the Proto workers.
type Queue interface {
	EnqueueContext(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error)
}

func EnqueueImport(ctx context.Context, q Queue, p ImportTaskPayload) error {
	return enqueue(ctx, q, TaskImport, p, QueueProtoImport)
}

func EnqueueImportDispatcher(ctx context.Context, q Queue) error {
	return enqueueRaw(ctx, q, TaskImportDispatcher, nil, QueueProtoImport)
}
func EnqueueValidation(ctx context.Context, q Queue, p ValidationTaskPayload) error {
	return enqueue(ctx, q, TaskValidate, p, QueueProtoValidation)
}
func EnqueueHistory(ctx context.Context, q Queue, p HistoryTaskPayload) error {
	return enqueue(ctx, q, TaskHistory, p, QueueProtoHistory)
}

func EnqueueHistoryDispatcher(ctx context.Context, q Queue) error {
	return enqueueRaw(ctx, q, TaskHistoryDispatcher, nil, QueueProtoHistory)
}

func EnqueueValidationDispatcher(ctx context.Context, q Queue) error {
	return enqueueRaw(ctx, q, TaskValidationDispatcher, nil, QueueProtoValidation)
}

func enqueue(ctx context.Context, q Queue, kind string, payload any, queue string) error {
	if q == nil {
		return errors.New("proto: queue unavailable")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("proto encode task: %w", err)
	}
	timeout := 15 * time.Minute
	if kind == TaskImport || kind == TaskHistory || kind == TaskBulk {
		timeout = 30 * time.Minute
	}
	_, err = q.EnqueueContext(ctx, asynq.NewTask(kind, b), asynq.Queue(queue), asynq.ProcessIn(time.Second), asynq.Unique(timeout+time.Second), asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Retention(0), asynq.Timeout(timeout))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func enqueueRaw(ctx context.Context, q Queue, kind string, payload []byte, queue string) error {
	if q == nil {
		return errors.New("proto: queue unavailable")
	}
	_, err := q.EnqueueContext(ctx, asynq.NewTask(kind, payload), asynq.Queue(queue), asynq.Unique(30*time.Second), asynq.MaxRetry(0), asynq.Retention(0), asynq.Timeout(30*time.Second))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func DecodeImportTask(task *asynq.Task) (ImportTaskPayload, error) {
	var p ImportTaskPayload
	if task == nil || json.Unmarshal(task.Payload(), &p) != nil || p.ImportID == 0 || p.Generation == 0 {
		return p, fmt.Errorf("proto: invalid import task: %w", asynq.SkipRetry)
	}
	return p, nil
}
func DecodeValidationTask(task *asynq.Task) (ValidationTaskPayload, error) {
	var p ValidationTaskPayload
	if task == nil || json.Unmarshal(task.Payload(), &p) != nil || p.ResourceID == 0 || p.OwnerUserID == 0 || p.CredentialRevision == 0 || p.ValidationGeneration == 0 {
		return p, fmt.Errorf("proto: invalid validation task: %w", asynq.SkipRetry)
	}
	return p, nil
}
func DecodeHistoryTask(task *asynq.Task) (HistoryTaskPayload, error) {
	var p HistoryTaskPayload
	if task == nil || json.Unmarshal(task.Payload(), &p) != nil || p.ResourceID == 0 || p.OwnerUserID == 0 || p.CredentialRevision == 0 || p.ValidationGeneration == 0 {
		return p, fmt.Errorf("proto: invalid history task: %w", asynq.SkipRetry)
	}
	return p, nil
}
