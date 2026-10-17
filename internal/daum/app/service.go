package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const (
	TaskImport                   = "daum:resource_import"
	TaskImportDispatcher         = "daum:resource_import_dispatcher"
	TaskValidate                 = "daum:validate"
	TaskValidationDispatcher     = "daum:validation_dispatcher"
	TaskHistory                  = "daum:history"
	TaskHistoryDispatcher        = "daum:history_dispatcher"
	TaskBulk                     = "daum:resource_bulk"
	TaskProjectHistory           = "daum:project_history"
	TaskProjectHistoryDispatcher = "daum:project_history_dispatcher"
	QueueDaumImport              = platform.QueueBackgroundDaumImport
	QueueDaumValidation          = platform.QueueBackgroundDaumValidation
	QueueDaumHistory             = platform.QueueBackgroundDaumHistory
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

// Queue is the narrow dependency needed by the Daum workers.
type Queue interface {
	EnqueueContext(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error)
}

func EnqueueImport(ctx context.Context, q Queue, p ImportTaskPayload) error {
	return enqueue(ctx, q, TaskImport, p, QueueDaumImport)
}

func EnqueueImportDispatcher(ctx context.Context, q Queue) error {
	return enqueueRaw(ctx, q, TaskImportDispatcher, nil, QueueDaumImport)
}
func EnqueueValidation(ctx context.Context, q Queue, p ValidationTaskPayload) error {
	return enqueue(ctx, q, TaskValidate, p, QueueDaumValidation)
}
func EnqueueValidationAccepted(ctx context.Context, q Queue, p ValidationTaskPayload) (bool, error) {
	return enqueueAccepted(ctx, q, TaskValidate, p, QueueDaumValidation)
}
func EnqueueHistory(ctx context.Context, q Queue, p HistoryTaskPayload) error {
	return enqueue(ctx, q, TaskHistory, p, QueueDaumHistory)
}

func EnqueueHistoryDispatcher(ctx context.Context, q Queue) error {
	return enqueueRaw(ctx, q, TaskHistoryDispatcher, nil, QueueDaumHistory)
}

func EnqueueValidationDispatcher(ctx context.Context, q Queue) error {
	return enqueueRaw(ctx, q, TaskValidationDispatcher, nil, QueueDaumValidation)
}

func enqueue(ctx context.Context, q Queue, kind string, payload any, queue string) error {
	_, err := enqueueAccepted(ctx, q, kind, payload, queue)
	return err
}
func enqueueAccepted(ctx context.Context, q Queue, kind string, payload any, queue string) (bool, error) {
	if q == nil {
		return false, errors.New("daum: queue unavailable")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("daum encode task: %w", err)
	}
	timeout := 15 * time.Minute
	if kind == TaskImport || kind == TaskBulk {
		timeout = 30 * time.Minute
	}
	retries := platform.BackgroundTaskMaxRetryValue()
	if kind == TaskHistory {
		timeout, retries = HistoryTaskTimeout(), HistoryTaskMaxRetry
	}
	_, err = q.EnqueueContext(ctx, asynq.NewTask(kind, b), asynq.Queue(queue), asynq.ProcessIn(time.Second), asynq.Unique(timeout+time.Second), asynq.MaxRetry(retries), asynq.Retention(0), asynq.Timeout(timeout))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	return err == nil, err
}

func enqueueRaw(ctx context.Context, q Queue, kind string, payload []byte, queue string) error {
	if q == nil {
		return errors.New("daum: queue unavailable")
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
		return p, fmt.Errorf("daum: invalid import task: %w", asynq.SkipRetry)
	}
	return p, nil
}
func DecodeValidationTask(task *asynq.Task) (ValidationTaskPayload, error) {
	var p ValidationTaskPayload
	if task == nil || json.Unmarshal(task.Payload(), &p) != nil || p.ResourceID == 0 || p.OwnerUserID == 0 || p.CredentialRevision == 0 || p.ValidationGeneration == 0 {
		return p, fmt.Errorf("daum: invalid validation task: %w", asynq.SkipRetry)
	}
	return p, nil
}
func DecodeHistoryTask(task *asynq.Task) (HistoryTaskPayload, error) {
	var p HistoryTaskPayload
	if task == nil || json.Unmarshal(task.Payload(), &p) != nil || p.ResourceID == 0 || p.OwnerUserID == 0 || p.CredentialRevision == 0 || p.ValidationGeneration == 0 {
		return p, fmt.Errorf("daum: invalid history task: %w", asynq.SkipRetry)
	}
	return p, nil
}

const HistoryTaskMaxRetry = 3

func HistoryTaskTimeout() time.Duration {
	return min(runtimeconfig.Duration("project_history_timeout_minutes", 20*time.Minute, time.Minute, 1), 2*time.Hour)
}
