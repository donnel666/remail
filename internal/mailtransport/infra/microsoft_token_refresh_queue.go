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

	MicrosoftTokenRefreshQueueName = platform.QueueBackgroundTokenRefresh

	microsoftTokenRefreshTaskTimeout       = 2 * time.Minute
	microsoftTokenRefreshDispatcherTimeout = time.Minute
)

type MicrosoftTokenRefreshQueue struct {
	client *asynq.Client
}

func NewMicrosoftTokenRefreshQueue(client *asynq.Client) *MicrosoftTokenRefreshQueue {
	return &MicrosoftTokenRefreshQueue{client: client}
}

func (q *MicrosoftTokenRefreshQueue) EnqueueMicrosoftTokenRefresh(ctx context.Context, task mailapp.MicrosoftTokenRefreshTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("microsoft token refresh queue is unavailable")
	}
	if task.ResourceID == 0 || task.Generation == 0 || task.ExpectedCredentialRevision == 0 {
		return false, fmt.Errorf("microsoft token refresh task identity is missing")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal microsoft token refresh task: %w", err)
	}
	_, err = q.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeMicrosoftTokenRefresh, payload),
		asynq.Queue(MicrosoftTokenRefreshQueueName),
		asynq.Unique(microsoftTokenRefreshTaskTimeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetry),
		asynq.Timeout(microsoftTokenRefreshTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue microsoft token refresh task: %w", err)
	}
	return true, nil
}

func (q *MicrosoftTokenRefreshQueue) EnqueueMicrosoftTokenRefreshDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("microsoft token refresh queue is unavailable")
	}
	uniqueTTL := microsoftTokenRefreshDispatcherTimeout
	if delay > 0 {
		uniqueTTL += delay
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(uniqueTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(microsoftTokenRefreshDispatcherTimeout),
		asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeMicrosoftTokenRefreshDispatcher, nil), options...)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue microsoft token refresh dispatcher: %w", err)
	}
	return nil
}
