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
	TypeAdminDomainBulk = "core:admin_domain_bulk"

	adminDomainBulkTaskMaxRetry   = platform.BackgroundTaskMaxRetry
	adminDomainBulkTaskTimeout    = time.Minute
	adminDomainBulkLeaseDuration  = 24 * time.Hour
	adminDomainBulkCleanupTimeout = 5 * time.Second
)

// AdminDomainBulkQueue is the Redis lease + Asynq transport for asynchronous
// domain publish/unpublish/delete batches. It mirrors the resource validation
// batch queue: a single-owner lease fences one cursor chain per BatchID, and
// each page task re-enqueues the next until the batch is drained.
type AdminDomainBulkQueue struct {
	client *asynq.Client
	redis  redis.UniversalClient
}

func NewAdminDomainBulkQueue(client *asynq.Client, redisClient redis.UniversalClient) *AdminDomainBulkQueue {
	return &AdminDomainBulkQueue{client: client, redis: redisClient}
}

// EnqueueAdminDomainBulk enqueues one batch page. It reports whether this call
// newly accepted the batch (i.e. the initial submission claimed the lease and
// enqueued page 0) so the caller writes the acceptance audit log exactly once —
// deduped re-submissions and per-page re-enqueues return false.
func (q *AdminDomainBulkQueue) EnqueueAdminDomainBulk(ctx context.Context, task coreapp.AdminDomainBulkTask) (bool, error) {
	if q == nil || q.client == nil || q.redis == nil {
		return false, fmt.Errorf("admin domain bulk queue is unavailable")
	}
	if task.BatchID == "" || task.OperatorUserID == 0 {
		return false, fmt.Errorf("admin domain bulk batch identity is required")
	}
	initial := strings.TrimSpace(task.ClaimToken) == "" && task.AfterID == 0
	if initial {
		task.ClaimToken = platform.NewUUIDV7String()
		claimed, err := q.redis.SetNX(ctx, adminDomainBulkLeaseKey(task.BatchID), task.ClaimToken, adminDomainBulkLeaseDuration).Result()
		if err != nil {
			return false, fmt.Errorf("claim admin domain bulk batch: %w", err)
		}
		if !claimed {
			return false, nil
		}
	} else {
		owned, err := q.RefreshAdminDomainBulk(ctx, task)
		if err != nil {
			return false, err
		}
		if !owned {
			return false, nil
		}
	}
	if strings.TrimSpace(task.ClaimToken) == "" {
		return false, fmt.Errorf("admin domain bulk batch claim token is required")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		if initial {
			q.releaseInitial(ctx, task)
		}
		return false, fmt.Errorf("marshal admin domain bulk task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeAdminDomainBulk, payload),
		asynq.Queue(platform.QueueResource),
		asynq.Unique(adminDomainBulkTaskTimeout),
		asynq.MaxRetry(adminDomainBulkTaskMaxRetry),
		asynq.Timeout(adminDomainBulkTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		// A task with this exact (claim, cursor) already exists: the page is
		// already queued, so the batch remains accepted and owned. A fresh initial
		// claim token is unique, so this only occurs on a page re-enqueue.
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		if initial {
			q.releaseInitial(ctx, task)
		}
		return false, fmt.Errorf("enqueue admin domain bulk task: %w", err)
	}
	return initial, nil
}

func (q *AdminDomainBulkQueue) RefreshAdminDomainBulk(ctx context.Context, task coreapp.AdminDomainBulkTask) (bool, error) {
	if q == nil || q.redis == nil || strings.TrimSpace(task.BatchID) == "" || strings.TrimSpace(task.ClaimToken) == "" {
		return false, nil
	}
	result, err := batchLeaseRefreshScript.Run(
		ctx, q.redis,
		[]string{adminDomainBulkLeaseKey(task.BatchID)},
		task.ClaimToken, adminDomainBulkLeaseDuration.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("refresh admin domain bulk lease: %w", err)
	}
	return result == 1, nil
}

func (q *AdminDomainBulkQueue) ReleaseAdminDomainBulk(ctx context.Context, task coreapp.AdminDomainBulkTask) error {
	if q == nil || q.redis == nil || strings.TrimSpace(task.BatchID) == "" || strings.TrimSpace(task.ClaimToken) == "" {
		return nil
	}
	if err := batchLeaseReleaseScript.Run(
		ctx, q.redis,
		[]string{adminDomainBulkLeaseKey(task.BatchID)}, task.ClaimToken,
	).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release admin domain bulk lease: %w", err)
	}
	return nil
}

func (q *AdminDomainBulkQueue) releaseInitial(ctx context.Context, task coreapp.AdminDomainBulkTask) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminDomainBulkCleanupTimeout)
	defer cancel()
	_ = q.ReleaseAdminDomainBulk(cleanupCtx, task)
}

func adminDomainBulkLeaseKey(batchID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(batchID)))
	return fmt.Sprintf("remail:core:admin-domain-bulk:%x", digest)
}

var _ coreapp.AdminDomainBulkQueue = (*AdminDomainBulkQueue)(nil)
