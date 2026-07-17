package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeMailmatchFetch                = "mailmatch:fetch"
	TypeMailmatchResourceFetch        = "mailmatch:resource_fetch"
	TypeMailmatchFetchDispatcher      = "mailmatch:fetch_dispatcher"
	TypeProjectHistoryScan            = "mailmatch:project_history_scan"
	TypeValidatedMicrosoftHistoryScan = "mailmatch:validated_microsoft_history_scan"
	TypeProjectHistoryDispatcher      = "mailmatch:project_history_dispatcher"

	mailmatchQueueName            = platform.QueueMailfetch
	mailmatchFetchTaskMaxRetry    = 0
	mailmatchFetchTaskTimeout     = 60 * time.Second
	mailmatchDispatchTaskTimeout  = 30 * time.Second
	projectHistoryTaskMaxRetry    = platform.BackgroundTaskMaxRetry
	projectHistoryTaskTimeout     = 20 * time.Minute
	projectHistoryDispatchTimeout = 30 * time.Second
)

type FetchQueue struct {
	client *asynq.Client
}

func NewFetchQueue(client *asynq.Client) *FetchQueue {
	return &FetchQueue{client: client}
}

func (q *FetchQueue) EnqueueFetch(ctx context.Context, task app.FetchTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("mailmatch fetch queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal mailmatch fetch task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeMailmatchFetch, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(mailmatchQueueName),
		asynq.TaskID(fmt.Sprintf("%s:%d", TypeMailmatchFetch, task.JobID)),
		asynq.MaxRetry(mailmatchFetchTaskMaxRetry),
		asynq.Timeout(mailmatchFetchTaskTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue mailmatch fetch task: %w", err)
	}
	return nil
}

func (q *FetchQueue) EnqueueResourceFetch(ctx context.Context, task app.ResourceFetchTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("mailmatch resource fetch queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal mailmatch resource fetch task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeMailmatchResourceFetch, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(mailmatchQueueName),
		asynq.TaskID(fmt.Sprintf("%s:%d:%s", TypeMailmatchResourceFetch, task.JobID, task.DispatchToken)),
		asynq.MaxRetry(mailmatchFetchTaskMaxRetry),
		asynq.Timeout(mailmatchFetchTaskTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue mailmatch resource fetch task: %w", err)
	}
	return nil
}

func (q *FetchQueue) EnqueueProjectHistoryScan(ctx context.Context, task app.ProjectHistoryScanTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("project history scan queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal project history scan task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeProjectHistoryScan, payload),
		asynq.Queue(platform.QueueBackgroundProjectHistory),
		asynq.TaskID(fmt.Sprintf("project-history:%d:%s", task.JobID, task.DispatchToken)),
		asynq.MaxRetry(projectHistoryTaskMaxRetry),
		asynq.Timeout(projectHistoryTaskTimeout),
	)
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue project history scan task: %w", err)
	}
	return nil
}

func (q *FetchQueue) EnqueueValidatedMicrosoftHistoryScan(ctx context.Context, task app.ValidatedMicrosoftHistoryScanTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("validated microsoft history scan queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal validated microsoft history scan task: %w", err)
	}
	taskID := fmt.Sprintf("validated-microsoft-history:%d", task.ResourceID)
	if requestID := strings.TrimSpace(task.RequestID); requestID != "" {
		taskID += ":" + requestID
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeValidatedMicrosoftHistoryScan, payload),
		asynq.Queue(platform.QueueBackgroundProjectHistory),
		asynq.TaskID(taskID),
		asynq.MaxRetry(projectHistoryTaskMaxRetry),
		asynq.Timeout(projectHistoryTaskTimeout),
	)
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
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
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundProjectHistory),
		asynq.TaskID(TypeProjectHistoryDispatcher),
		asynq.MaxRetry(0),
		asynq.Timeout(projectHistoryDispatchTimeout),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeProjectHistoryDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue project history dispatcher: %w", err)
	}
	return nil
}

func (q *FetchQueue) EnqueueFetchDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("mailmatch fetch dispatcher queue is unavailable")
	}
	task := asynq.NewTask(TypeMailmatchFetchDispatcher, nil)
	options := []asynq.Option{
		asynq.Queue(mailmatchQueueName),
		asynq.TaskID(TypeMailmatchFetchDispatcher),
		asynq.MaxRetry(0),
		asynq.Timeout(mailmatchDispatchTaskTimeout),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, task, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue mailmatch fetch dispatcher task: %w", err)
	}
	return nil
}
