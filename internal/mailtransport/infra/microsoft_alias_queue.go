package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/hibiken/asynq"
)

const (
	TypeMicrosoftAlias           = "mailtransport:microsoft_alias"
	TypeMicrosoftAliasDispatcher = "mailtransport:microsoft_alias_dispatcher"
	MicrosoftAliasQueueName      = "background_alias"

	microsoftAliasTaskTimeout     = 20 * time.Minute
	microsoftAliasDispatchTimeout = time.Minute
)

type MicrosoftAliasQueue struct {
	client *asynq.Client
}

func NewMicrosoftAliasQueue(client *asynq.Client) *MicrosoftAliasQueue {
	return &MicrosoftAliasQueue{client: client}
}

func (q *MicrosoftAliasQueue) EnqueueMicrosoftAlias(ctx context.Context, task mailapp.MicrosoftAliasTask) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("microsoft alias queue is unavailable")
	}
	if task.ResourceID == 0 || task.DispatchToken == "" {
		return fmt.Errorf("microsoft alias task identity is missing")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal microsoft alias task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeMicrosoftAlias, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(MicrosoftAliasQueueName),
		asynq.TaskID(fmt.Sprintf(
			"%s:%d:%s",
			TypeMicrosoftAlias,
			task.ResourceID,
			task.DispatchToken,
		)),
		asynq.MaxRetry(0),
		asynq.Timeout(microsoftAliasTaskTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue microsoft alias task: %w", err)
	}
	return nil
}

func (q *MicrosoftAliasQueue) EnqueueMicrosoftAliasDispatcher(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("microsoft alias queue is unavailable")
	}
	options := []asynq.Option{
		asynq.Queue("default"),
		asynq.Unique(15 * time.Second),
		asynq.MaxRetry(0),
		asynq.Timeout(microsoftAliasDispatchTimeout),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynq.NewTask(TypeMicrosoftAliasDispatcher, nil), options...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue microsoft alias dispatcher: %w", err)
	}
	return nil
}
