package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const (
	TypeMailmatchFetch                        = "mailmatch:fetch"
	TypeMailmatchPickupFetch                  = "mailmatch:pickup_fetch"
	TypeMailmatchPickupRequestFetch           = "mailmatch:pickup_request_fetch_v2"
	TypeMailmatchAdminResourceFetch           = "mailmatch:admin_resource_fetch"
	TypeMailmatchAdminResourceFetchDispatcher = "mailmatch:admin_resource_fetch_dispatcher"
	TypeMailmatchResourceHistory              = "mailmatch:resource_history"
	TypeMailmatchResourceHistoryDispatcher    = "mailmatch:resource_history_dispatcher"
	TypeProjectHistoryScan                    = "mailmatch:project_history_scan"
	TypeValidatedMicrosoftHistoryScan         = "mailmatch:validated_microsoft_history_scan"
	TypeProjectHistoryDispatcher              = "mailmatch:project_history_dispatcher"

	mailmatchQueueName            = platform.QueueMailfetch
	pickupRequestFetchTaskTimeout = 2 * time.Minute
	resourceFetchTaskMaxRetry     = 3
	mailmatchFetchTaskTimeout     = 20 * time.Minute
	mailmatchDispatchTaskTimeout  = 5 * time.Minute
	ValidatedHistoryTaskMaxRetry  = 3
	projectHistoryTaskTimeout     = 20 * time.Minute
	projectHistoryDispatchTimeout = 30 * time.Second

	maxPickupRequestFetchTaskTimeout = 30 * time.Minute
	maxMailmatchFetchTaskTimeout     = time.Hour
	maxProjectHistoryTaskTimeout     = 2 * time.Hour
)

type FetchQueue struct {
	client *asynq.Client
}

func NewFetchQueue(client *asynq.Client) *FetchQueue {
	return &FetchQueue{client: client}
}

func (q *FetchQueue) EnqueuePickupRequest(ctx context.Context, task app.PickupRequestFetchTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("mailmatch pickup request queue is unavailable")
	}
	timeout := min(runtimeconfig.Duration("pickup_request_fetch_timeout_minutes", pickupRequestFetchTaskTimeout, time.Minute, 1), maxPickupRequestFetchTaskTimeout)
	if task.ExpiresAt.IsZero() && !task.RequestedAt.IsZero() {
		task.ExpiresAt = task.RequestedAt.Add(timeout)
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal mailmatch pickup request task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeMailmatchPickupRequestFetch, payload),
		asynq.Queue(mailmatchQueueName),
		asynq.Unique(timeout),
		asynq.MaxRetry(0),
		asynq.Timeout(timeout),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue mailmatch pickup request task: %w", err)
	}
	return true, nil
}

func (q *FetchQueue) EnqueueAdminResourceFetch(ctx context.Context, task app.AdminResourceFetchTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("admin resource fetch queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal admin resource fetch task: %w", err)
	}
	timeout := resourceFetchTaskTimeout()
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(TypeMailmatchAdminResourceFetch, payload),
		asynq.Queue(platform.QueueDefault), asynq.Unique(timeout),
		asynq.MaxRetry(resourceFetchTaskMaxRetry), asynq.Timeout(timeout), asynq.Retention(0))
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue admin resource fetch task: %w", err)
	}
	return true, nil
}

func (q *FetchQueue) EnqueueResourceHistory(ctx context.Context, task app.ResourceHistoryTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("resource history queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal resource history task: %w", err)
	}
	timeout := resourceFetchTaskTimeout()
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(TypeMailmatchResourceHistory, payload),
		asynq.Queue(platform.QueueBackgroundProjectHistory), asynq.Unique(timeout),
		asynq.MaxRetry(resourceFetchTaskMaxRetry), asynq.Timeout(timeout), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue resource history task: %w", err)
	}
	return true, nil
}

func resourceFetchTaskTimeout() time.Duration {
	return min(runtimeconfig.Duration("mailmatch_fetch_timeout_minutes", mailmatchFetchTaskTimeout, time.Minute, 1), maxMailmatchFetchTaskTimeout)
}

func (q *FetchQueue) EnqueueProjectHistoryScan(ctx context.Context, task app.ProjectHistoryScanTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("project history scan queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal project history scan task: %w", err)
	}
	timeout := min(runtimeconfig.Duration("project_history_timeout_minutes", projectHistoryTaskTimeout, time.Minute, 1), maxProjectHistoryTaskTimeout)
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeProjectHistoryScan, payload),
		asynq.Queue(platform.QueueBackgroundProjectHistory),
		asynq.Unique(timeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(timeout),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enqueue project history scan task: %w", err)
	}
	return true, nil
}

func (q *FetchQueue) EnqueueValidatedMicrosoftHistoryScan(ctx context.Context, task app.ValidatedMicrosoftHistoryScanTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("validated microsoft history scan queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal validated microsoft history scan task: %w", err)
	}
	timeout := min(runtimeconfig.Duration("project_history_timeout_minutes", projectHistoryTaskTimeout, time.Minute, 1), maxProjectHistoryTaskTimeout)
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeValidatedMicrosoftHistoryScan, payload),
		asynq.Queue(platform.QueueBackgroundProjectHistory),
		asynq.Unique(timeout),
		asynq.MaxRetry(ValidatedHistoryTaskMaxRetry),
		asynq.Timeout(timeout),
		asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue validated microsoft history scan task: %w", err)
	}
	return nil
}

func (q *FetchQueue) EnqueueProjectHistoryDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("project history dispatcher queue is unavailable")
	}
	uniqueTTL := projectHistoryDispatchTimeout
	if delay > 0 {
		uniqueTTL += delay
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundProjectHistory),
		asynq.Unique(uniqueTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(projectHistoryDispatchTimeout),
		asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeProjectHistoryDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue project history dispatcher: %w", err)
	}
	return nil
}

func (q *FetchQueue) EnqueueAdminResourceFetchDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("admin resource fetch dispatcher queue is unavailable")
	}
	return q.enqueueResourceDispatcher(ctx, TypeMailmatchAdminResourceFetchDispatcher, platform.QueueDefault, delay)
}

func (q *FetchQueue) EnqueueResourceHistoryDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("resource history dispatcher queue is unavailable")
	}
	return q.enqueueResourceDispatcher(ctx, TypeMailmatchResourceHistoryDispatcher, platform.QueueBackgroundProjectHistory, delay)
}

func (q *FetchQueue) enqueueResourceDispatcher(ctx context.Context, taskType, queue string, delay time.Duration) error {
	task := asynq.NewTask(taskType, nil)
	timeout := runtimeconfig.Duration("fetch_dispatcher_timeout_seconds", mailmatchDispatchTaskTimeout, time.Second, 1)
	uniqueTTL := timeout
	if delay > 0 {
		uniqueTTL += delay
	}
	options := []asynq.Option{
		asynq.Queue(queue),
		asynq.Unique(uniqueTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(timeout),
		asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, task, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue %s task: %w", taskType, err)
	}
	return nil
}
