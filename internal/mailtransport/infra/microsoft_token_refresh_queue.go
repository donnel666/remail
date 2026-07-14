package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeMicrosoftTokenRefresh           = "mailtransport:microsoft_token_refresh"
	TypeMicrosoftTokenRefreshDispatcher = "mailtransport:microsoft_token_refresh_dispatcher"

	MicrosoftTokenRefreshQueueName = platform.QueueBackgroundValidation

	microsoftTokenRefreshTaskTimeout       = 2 * time.Minute
	microsoftTokenRefreshDispatcherTimeout = time.Minute
)

type MicrosoftTokenRefreshQueue struct {
	client *asynq.Client
}

func NewMicrosoftTokenRefreshQueue(client *asynq.Client) *MicrosoftTokenRefreshQueue {
	return &MicrosoftTokenRefreshQueue{client: client}
}

func (q *MicrosoftTokenRefreshQueue) EnqueueMicrosoftTokenRefresh(ctx context.Context, task mailapp.MicrosoftTokenRefreshTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("microsoft token refresh queue is unavailable")
	}
	if task.JobID == 0 || task.ResourceID == 0 || task.DispatchToken == "" {
		return fmt.Errorf("microsoft token refresh task identity is missing")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal microsoft token refresh task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeMicrosoftTokenRefresh, payload),
		asynq.Queue(MicrosoftTokenRefreshQueueName),
		asynq.TaskID(fmt.Sprintf("%s:%d:%s", TypeMicrosoftTokenRefresh, task.JobID, task.DispatchToken)),
		asynq.MaxRetry(0),
		asynq.Timeout(microsoftTokenRefreshTaskTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue microsoft token refresh task: %w", err)
	}
	return nil
}

func (q *MicrosoftTokenRefreshQueue) EnqueueMicrosoftTokenRefreshDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("microsoft token refresh queue is unavailable")
	}
	options := []asynq.Option{
		asynq.Queue("default"),
		asynq.Unique(15 * time.Second),
		asynq.MaxRetry(0),
		asynq.Timeout(microsoftTokenRefreshDispatcherTimeout),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeMicrosoftTokenRefreshDispatcher, nil), options...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue microsoft token refresh dispatcher: %w", err)
	}
	return nil
}
