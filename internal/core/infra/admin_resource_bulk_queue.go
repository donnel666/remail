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
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	TypeAdminResourceBulk = "core:admin_resource_bulk"

	AdminResourceBulkQueueName      = platform.QueueResource
	adminResourceBulkTaskTimeout    = 30 * time.Minute
	adminResourceBulkLeaseDuration  = 24 * time.Hour
	adminResourceBulkCleanupTimeout = 5 * time.Second
	adminResourceBulkTaskMaxRetry   = platform.BackgroundTaskMaxRetry
)

type AdminResourceBulkQueue struct {
	client *asynq.Client
	redis  redis.UniversalClient
}

func NewAdminResourceBulkQueue(client *asynq.Client, redisClient redis.UniversalClient) *AdminResourceBulkQueue {
	return &AdminResourceBulkQueue{client: client, redis: redisClient}
}

func (q *AdminResourceBulkQueue) EnqueueAdminResourceBulk(ctx context.Context, task coreapp.AdminResourceBulkTask) (bool, error) {
	if q == nil || q.client == nil || q.redis == nil {
		return false, fmt.Errorf("admin resource bulk queue is unavailable")
	}
	if strings.TrimSpace(task.BatchID) == "" || strings.TrimSpace(task.RequestFingerprint) == "" || task.OperatorUserID == 0 {
		return false, fmt.Errorf("admin resource bulk identity is required")
	}
	initial := strings.TrimSpace(task.ClaimToken) == "" && task.AfterID == 0
	if initial {
		task.ClaimToken = platform.NewUUIDV7String()
		value := adminResourceBulkLeaseValue(task)
		claimed, err := q.redis.SetNX(ctx, adminResourceBulkLeaseKey(task.BatchID), value, adminResourceBulkLeaseDuration).Result()
		if err != nil {
			return false, fmt.Errorf("claim admin resource bulk batch: %w", err)
		}
		if !claimed {
			stored, getErr := q.redis.Get(ctx, adminResourceBulkLeaseKey(task.BatchID)).Result()
			if getErr != nil {
				return false, fmt.Errorf("read admin resource bulk lease: %w", getErr)
			}
			if !strings.HasPrefix(stored, task.RequestFingerprint+":") {
				return false, domain.ErrResourceIdempotencyConflict
			}
			return false, nil
		}
	} else {
		owned, err := q.RefreshAdminResourceBulk(ctx, task)
		if err != nil {
			return false, err
		}
		if !owned {
			return false, nil
		}
	}
	payload, err := json.Marshal(task)
	if err != nil {
		if initial {
			q.releaseInitial(ctx, task)
		}
		return false, fmt.Errorf("marshal admin resource bulk task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeAdminResourceBulk, payload),
		asynq.Queue(AdminResourceBulkQueueName),
		asynq.MaxRetry(adminResourceBulkTaskMaxRetry),
		asynq.Unique(adminResourceBulkTaskTimeout),
		asynq.Timeout(adminResourceBulkTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		if initial {
			q.releaseInitial(ctx, task)
		}
		return false, fmt.Errorf("enqueue admin resource bulk task: %w", err)
	}
	return true, nil
}

func (q *AdminResourceBulkQueue) RefreshAdminResourceBulk(ctx context.Context, task coreapp.AdminResourceBulkTask) (bool, error) {
	if q == nil || q.redis == nil || strings.TrimSpace(task.BatchID) == "" || strings.TrimSpace(task.ClaimToken) == "" || strings.TrimSpace(task.RequestFingerprint) == "" {
		return false, nil
	}
	result, err := batchLeaseRefreshScript.Run(
		ctx,
		q.redis,
		[]string{adminResourceBulkLeaseKey(task.BatchID)},
		adminResourceBulkLeaseValue(task),
		adminResourceBulkLeaseDuration.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("refresh admin resource bulk lease: %w", err)
	}
	return result == 1, nil
}

func (q *AdminResourceBulkQueue) ReleaseAdminResourceBulk(ctx context.Context, task coreapp.AdminResourceBulkTask) error {
	if q == nil || q.redis == nil || strings.TrimSpace(task.BatchID) == "" || strings.TrimSpace(task.ClaimToken) == "" || strings.TrimSpace(task.RequestFingerprint) == "" {
		return nil
	}
	if err := batchLeaseReleaseScript.Run(
		ctx,
		q.redis,
		[]string{adminResourceBulkLeaseKey(task.BatchID)},
		adminResourceBulkLeaseValue(task),
	).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release admin resource bulk lease: %w", err)
	}
	return nil
}

func (q *AdminResourceBulkQueue) releaseInitial(ctx context.Context, task coreapp.AdminResourceBulkTask) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminResourceBulkCleanupTimeout)
	defer cancel()
	_ = q.ReleaseAdminResourceBulk(cleanupCtx, task)
}

func adminResourceBulkLeaseKey(batchID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(batchID)))
	return fmt.Sprintf("remail:core:admin-resource-bulk:%x", digest)
}

func adminResourceBulkLeaseValue(task coreapp.AdminResourceBulkTask) string {
	return strings.TrimSpace(task.RequestFingerprint) + ":" + strings.TrimSpace(task.ClaimToken)
}

var _ coreapp.AdminResourceBulkQueue = (*AdminResourceBulkQueue)(nil)
