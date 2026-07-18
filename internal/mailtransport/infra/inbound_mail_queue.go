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
	TypeInboundProcess  = "mailtransport:inbound_process"
	TypeInboundDispatch = "mailtransport:inbound_dispatch"

	inboundTaskMaxRetry = platform.BackgroundTaskMaxRetry
	inboundTaskTimeout  = 2 * time.Minute
)

type InboundMailQueue struct {
	client *asynq.Client
}

func (q *InboundMailQueue) EnqueueInboundDispatch(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("inbound mail queue is unavailable")
	}
	asynqTask := asynq.NewTask(TypeInboundDispatch, nil)
	uniqueTTL := dispatchTaskTimeout
	if delay > 0 {
		uniqueTTL += delay
	}
	options := []asynq.Option{
		asynq.Queue(mailQueueName),
		asynq.Unique(uniqueTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(dispatchTaskTimeout),
		asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := q.client.EnqueueContext(ctx, asynqTask, options...)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return nil
		}
		return fmt.Errorf("enqueue inbound mail dispatcher task: %w", err)
	}
	return nil
}

func NewInboundMailQueue(client *asynq.Client) *InboundMailQueue {
	return &InboundMailQueue{client: client}
}

func (q *InboundMailQueue) EnqueueInboundProcess(ctx context.Context, task mailapp.InboundProcessTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("inbound mail queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal inbound mail task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeInboundProcess, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(mailQueueName),
		asynq.Unique(inboundTaskTimeout),
		asynq.MaxRetry(inboundTaskMaxRetry),
		asynq.Timeout(inboundTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue inbound mail task: %w", err)
	}
	return true, nil
}
