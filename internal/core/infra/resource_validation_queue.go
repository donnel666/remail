package infra

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	TypeResourceValidation           = "core:resource_validation"
	TypeResourceValidationBatch      = "core:resource_validation_batch"
	TypeResourceValidationDispatcher = "core:resource_validation_dispatcher"

	ResourceValidationQueueName     = platform.QueueBackgroundValidation
	validationTaskMaxRetry          = 3
	validationBatchTaskMaxRetry     = platform.BackgroundTaskMaxRetry
	validationTaskTimeout           = 15 * time.Minute
	validationBatchTaskTimeout      = time.Minute
	validationDispatcherTaskTimeout = 30 * time.Second
	validationBatchLeaseDuration    = 24 * time.Hour
	validationBatchCleanupTimeout   = 5 * time.Second
)

type ResourceValidationQueue struct {
	client *asynq.Client
	redis  redis.UniversalClient
}

func NewResourceValidationQueue(client *asynq.Client, redisClient redis.UniversalClient) *ResourceValidationQueue {
	return &ResourceValidationQueue{client: client, redis: redisClient}
}

func (q *ResourceValidationQueue) EnqueueResourceValidation(ctx context.Context, task coreapp.ResourceValidationTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("resource validation queue is unavailable")
	}
	if task.ResourceID == 0 || task.OwnerUserID == 0 || !coreappValidationTaskTypeValid(task) {
		return fmt.Errorf("resource validation task identity is required")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal resource validation task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeResourceValidation, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(ResourceValidationQueueName),
		asynq.TaskID(fmt.Sprintf("resource-validation:%s:%d:%d", task.ResourceType, task.ResourceID, task.ExpectedCredentialRevision)),
		asynq.MaxRetry(validationTaskMaxRetry),
		asynq.Timeout(validationTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue resource validation task: %w", err)
	}
	return nil
}

func coreappValidationTaskTypeValid(task coreapp.ResourceValidationTask) bool {
	return task.ResourceType == "microsoft" || task.ResourceType == "domain"
}

func (q *ResourceValidationQueue) EnqueueResourceValidationBatch(ctx context.Context, task coreapp.ResourceValidationBatchTask) error {
	if q == nil || q.client == nil || q.redis == nil {
		return fmt.Errorf("resource validation queue is unavailable")
	}
	if task.BatchID == "" || task.OwnerUserID == 0 {
		return fmt.Errorf("resource validation batch identity is required")
	}
	initial := strings.TrimSpace(task.ClaimToken) == "" && task.AfterID == 0
	if initial {
		task.ClaimToken = platform.NewUUIDV7String()
		claimed, err := q.redis.SetNX(ctx, resourceValidationBatchLeaseKey(task.BatchID), task.ClaimToken, validationBatchLeaseDuration).Result()
		if err != nil {
			return fmt.Errorf("claim resource validation batch: %w", err)
		}
		if !claimed {
			return nil
		}
	} else {
		owned, err := q.RefreshResourceValidationBatch(ctx, task)
		if err != nil {
			return err
		}
		if !owned {
			return nil
		}
	}
	if strings.TrimSpace(task.ClaimToken) == "" {
		return fmt.Errorf("resource validation batch claim token is required")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		if initial {
			q.releaseInitialResourceValidationBatch(ctx, task)
		}
		return fmt.Errorf("marshal resource validation batch task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeResourceValidationBatch, payload),
		asynq.Queue(platform.QueueResource),
		asynq.TaskID(resourceValidationBatchTaskID(task)),
		asynq.MaxRetry(validationBatchTaskMaxRetry),
		asynq.Timeout(validationBatchTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if initial {
			q.releaseInitialResourceValidationBatch(ctx, task)
		}
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue resource validation batch task: %w", err)
	}
	return nil
}

func (q *ResourceValidationQueue) RefreshResourceValidationBatch(ctx context.Context, task coreapp.ResourceValidationBatchTask) (bool, error) {
	if q == nil || q.redis == nil || strings.TrimSpace(task.BatchID) == "" || strings.TrimSpace(task.ClaimToken) == "" {
		return false, nil
	}
	result, err := refreshResourceValidationBatchLease.Run(
		ctx,
		q.redis,
		[]string{resourceValidationBatchLeaseKey(task.BatchID)},
		task.ClaimToken,
		validationBatchLeaseDuration.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("refresh resource validation batch lease: %w", err)
	}
	return result == 1, nil
}

func (q *ResourceValidationQueue) ReleaseResourceValidationBatch(ctx context.Context, task coreapp.ResourceValidationBatchTask) error {
	if q == nil || q.redis == nil || strings.TrimSpace(task.BatchID) == "" || strings.TrimSpace(task.ClaimToken) == "" {
		return nil
	}
	if err := releaseResourceValidationBatchLease.Run(
		ctx,
		q.redis,
		[]string{resourceValidationBatchLeaseKey(task.BatchID)},
		task.ClaimToken,
	).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release resource validation batch lease: %w", err)
	}
	return nil
}

func (q *ResourceValidationQueue) releaseInitialResourceValidationBatch(ctx context.Context, task coreapp.ResourceValidationBatchTask) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), validationBatchCleanupTimeout)
	defer cancel()
	_ = q.ReleaseResourceValidationBatch(cleanupCtx, task)
}

func resourceValidationBatchLeaseKey(batchID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(batchID)))
	return fmt.Sprintf("remail:core:resource-validation-batch:%x", digest)
}

func resourceValidationBatchTaskID(task coreapp.ResourceValidationBatchTask) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(task.BatchID)))
	return fmt.Sprintf("resource-validation-batch:%x:%s:%d", digest, task.ClaimToken, task.AfterID)
}

var refreshResourceValidationBatchLease = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
    return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

var releaseResourceValidationBatchLease = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
    return 0
end
return redis.call('DEL', KEYS[1])
`)

func (q *ResourceValidationQueue) EnqueueResourceValidationDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("resource validation queue is unavailable")
	}
	asynqTask := asynq.NewTask(TypeResourceValidationDispatcher, nil)
	options := []asynq.Option{
		asynq.Queue("default"),
		// Asynq releases the uniqueness key as soon as the dispatcher finishes.
		// Keeping the lease for the full task timeout prevents a slow scan from
		// spawning overlapping dispatcher tasks every two seconds.
		asynq.Unique(validationDispatcherTaskTimeout),
		asynq.MaxRetry(0),
		asynq.Timeout(validationDispatcherTaskTimeout),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynqTask, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue resource validation dispatcher task: %w", err)
	}
	return nil
}
