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
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	TypeAdminResourceBulk = "core:admin_resource_bulk"

	AdminResourceBulkQueueName      = platform.QueueResource
	adminResourceBulkTaskTimeout    = 30 * time.Minute
	adminResourceBulkLeaseDuration  = 24 * time.Hour
	adminResourceBulkStatusTTL      = 24 * time.Hour
	adminResourceBulkCleanupTimeout = 5 * time.Second
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
		if err := q.ensureAdminResourceBulkStatus(ctx, task, "queued"); err != nil {
			q.releaseInitial(ctx, task)
			return false, err
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
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
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
	if result != 1 {
		return false, nil
	}
	if err := q.ensureAdminResourceBulkStatus(ctx, task, "running"); err != nil {
		return false, err
	}
	if err := adminResourceBulkStatusRunningScript.Run(
		ctx,
		q.redis,
		[]string{governanceinfra.AdminResourceBulkStatusKey(task.CommandID)},
		time.Now().UTC().UnixMilli(),
		adminResourceBulkStatusTTL.Milliseconds(),
	).Err(); err != nil {
		return false, fmt.Errorf("mark admin resource bulk task running: %w", err)
	}
	return true, nil
}

func (q *AdminResourceBulkQueue) RecordAdminResourceBulkPage(ctx context.Context, task coreapp.AdminResourceBulkTask, page coreapp.AdminResourceBulkPageResult) error {
	if q == nil || q.redis == nil || task.CommandID == 0 || page.Affected < 0 || page.Skipped < 0 || page.AfterID < task.AfterID {
		return fmt.Errorf("admin resource bulk progress is invalid")
	}
	arguments := []any{
		task.AfterID,
		page.AfterID,
		page.ThroughID,
		page.Affected + page.Skipped,
		page.Affected,
		page.Skipped,
		boolInt(page.Done),
		time.Now().UTC().UnixMilli(),
		adminResourceBulkStatusTTL.Milliseconds(),
	}
	for reason, count := range page.ReasonCounts {
		reason = strings.TrimSpace(reason)
		if reason == "" || count <= 0 {
			continue
		}
		arguments = append(arguments, reason, count)
	}
	result, err := adminResourceBulkStatusAdvanceScript.Run(
		ctx,
		q.redis,
		[]string{governanceinfra.AdminResourceBulkStatusKey(task.CommandID)},
		arguments...,
	).Int64()
	if err != nil {
		return fmt.Errorf("record admin resource bulk progress: %w", err)
	}
	if result < 0 {
		return fmt.Errorf("record admin resource bulk progress: stale checkpoint")
	}
	return nil
}

func (q *AdminResourceBulkQueue) FailAdminResourceBulk(ctx context.Context, task coreapp.AdminResourceBulkTask) error {
	if q == nil || q.redis == nil || task.CommandID == 0 {
		return nil
	}
	if err := q.ensureAdminResourceBulkStatus(ctx, task, "running"); err != nil {
		return err
	}
	if err := adminResourceBulkStatusFailedScript.Run(
		ctx,
		q.redis,
		[]string{governanceinfra.AdminResourceBulkStatusKey(task.CommandID)},
		time.Now().UTC().UnixMilli(),
		adminResourceBulkStatusTTL.Milliseconds(),
	).Err(); err != nil {
		return fmt.Errorf("mark admin resource bulk task failed: %w", err)
	}
	return nil
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
	_ = q.redis.Del(cleanupCtx, governanceinfra.AdminResourceBulkStatusKey(task.CommandID)).Err()
}

func (q *AdminResourceBulkQueue) ensureAdminResourceBulkStatus(ctx context.Context, task coreapp.AdminResourceBulkTask, status string) error {
	total := 0
	if task.Selection.Mode == coreapp.AdminResourceBulkIDs {
		total = len(task.Selection.ResourceIDs)
	}
	now := time.Now().UTC().UnixMilli()
	if err := adminResourceBulkStatusInitScript.Run(
		ctx,
		q.redis,
		[]string{governanceinfra.AdminResourceBulkStatusKey(task.CommandID)},
		string(task.Action),
		status,
		total,
		now,
		task.AfterID,
		task.ThroughID,
		adminResourceBulkStatusTTL.Milliseconds(),
	).Err(); err != nil {
		return fmt.Errorf("initialize admin resource bulk task status: %w", err)
	}
	return nil
}

func adminResourceBulkLeaseKey(batchID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(batchID)))
	return fmt.Sprintf("remail:core:admin-resource-bulk:%x", digest)
}

func adminResourceBulkLeaseValue(task coreapp.AdminResourceBulkTask) string {
	return strings.TrimSpace(task.RequestFingerprint) + ":" + strings.TrimSpace(task.ClaimToken)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var adminResourceBulkStatusInitScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
    redis.call('HSET', KEYS[1],
        'action', ARGV[1],
        'status', ARGV[2],
        'total', ARGV[3],
        'processed', 0,
        'succeeded', 0,
        'skipped', 0,
        'failed', 0,
        'attempts', 0,
        'max_attempts', 1,
        'after_id', ARGV[5],
        'through_id', ARGV[6],
        'queued_at', ARGV[4],
        'updated_at', ARGV[4])
end
redis.call('PEXPIRE', KEYS[1], ARGV[7])
return 1
`)

var adminResourceBulkStatusRunningScript = redis.NewScript(`
local status = redis.call('HGET', KEYS[1], 'status')
if status ~= 'succeeded' and status ~= 'failed' and status ~= 'canceled' then
    redis.call('HSET', KEYS[1], 'status', 'running', 'attempts', 1, 'updated_at', ARGV[1])
    if not redis.call('HGET', KEYS[1], 'started_at') then
        redis.call('HSET', KEYS[1], 'started_at', ARGV[1])
    end
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

var adminResourceBulkStatusAdvanceScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
    return -2
end
local checkpoint = tonumber(redis.call('HGET', KEYS[1], 'after_id') or '0')
local expected = tonumber(ARGV[1])
if checkpoint > expected then
    return 0
end
if checkpoint ~= expected then
    return -1
end
local processed = redis.call('HINCRBY', KEYS[1], 'processed', ARGV[4])
redis.call('HINCRBY', KEYS[1], 'succeeded', ARGV[5])
redis.call('HINCRBY', KEYS[1], 'skipped', ARGV[6])
for index = 10, #ARGV, 2 do
    redis.call('HINCRBY', KEYS[1], 'reason:' .. ARGV[index], ARGV[index + 1])
end
redis.call('HSET', KEYS[1],
    'after_id', ARGV[2],
    'through_id', ARGV[3],
    'attempts', 1,
    'updated_at', ARGV[8])
if tonumber(ARGV[7]) == 1 then
    redis.call('HSET', KEYS[1], 'status', 'succeeded', 'finished_at', ARGV[8])
    local total = tonumber(redis.call('HGET', KEYS[1], 'total') or '0')
    if total < tonumber(processed) then
        redis.call('HSET', KEYS[1], 'total', processed)
    end
else
    redis.call('HSET', KEYS[1], 'status', 'running')
end
redis.call('PEXPIRE', KEYS[1], ARGV[9])
return 1
`)

var adminResourceBulkStatusFailedScript = redis.NewScript(`
local status = redis.call('HGET', KEYS[1], 'status')
if status ~= 'succeeded' and status ~= 'failed' and status ~= 'canceled' then
    local processed = tonumber(redis.call('HGET', KEYS[1], 'processed') or '0')
    local total = tonumber(redis.call('HGET', KEYS[1], 'total') or '0')
    if total < processed + 1 then
        total = processed + 1
    end
    redis.call('HSET', KEYS[1],
        'status', 'failed',
        'failed', 1,
        'total', total,
        'attempts', 1,
        'finished_at', ARGV[1],
        'updated_at', ARGV[1])
    redis.call('HINCRBY', KEYS[1], 'reason:internal_error', 1)
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

var _ coreapp.AdminResourceBulkQueue = (*AdminResourceBulkQueue)(nil)
