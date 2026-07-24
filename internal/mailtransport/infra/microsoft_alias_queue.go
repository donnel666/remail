package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
)

const (
	TypeMicrosoftAlias           = "mailtransport:microsoft_alias"
	TypeMicrosoftAliasDispatcher = "mailtransport:microsoft_alias_dispatcher"
	MicrosoftAliasQueueName      = platform.QueueBackgroundAlias

	microsoftAliasTaskTimeout     = 20 * time.Minute
	microsoftAliasDispatchTimeout = time.Minute
)

type MicrosoftAliasQueue struct {
	client *asynq.Client
}

func microsoftAliasTimeout() time.Duration {
	const defaultRecoveryWait = 90 * time.Second
	wait := min(runtimeconfig.Duration("password_recovery_code_wait_seconds", defaultRecoveryWait, time.Second, 1), 30*time.Minute)
	return microsoftAliasTaskTimeout + max(time.Duration(0), wait-defaultRecoveryWait)
}

func NewMicrosoftAliasQueue(client *asynq.Client) *MicrosoftAliasQueue {
	return &MicrosoftAliasQueue{client: client}
}

func (q *MicrosoftAliasQueue) EnqueueMicrosoftAlias(ctx context.Context, task mailapp.MicrosoftAliasTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("microsoft alias queue is unavailable")
	}
	if task.ResourceID == 0 || task.Generation == 0 {
		return false, fmt.Errorf("microsoft alias task identity is missing")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal microsoft alias task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeMicrosoftAlias, payload)
	taskTimeout := microsoftAliasTimeout()
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(MicrosoftAliasQueueName),
		asynq.Unique(taskTimeout),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetry),
		asynq.Timeout(taskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue microsoft alias task: %w", err)
	}
	return true, nil
}

func (q *MicrosoftAliasQueue) EnqueueMicrosoftAliasDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("microsoft alias queue is unavailable")
	}
	uniqueTTL := microsoftAliasDispatchTimeout
	if delay > 0 {
		uniqueTTL += delay
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueDefault),
		asynq.Unique(uniqueTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(microsoftAliasDispatchTimeout),
		asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeMicrosoftAliasDispatcher, nil), options...)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue microsoft alias dispatcher: %w", err)
	}
	return nil
}
