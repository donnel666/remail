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
	TypeOutboundSend     = "mailtransport:outbound_send"
	TypeOutboundDispatch = "mailtransport:outbound_dispatch"
	mailQueueName        = platform.QueueMailtransport
	outboundTaskMaxRetry = platform.BackgroundTaskMaxRetry
	outboundTaskTimeout  = 3 * time.Minute
	dispatchTaskTimeout  = 30 * time.Second
)

type OutboundMailQueue struct {
	client *asynq.Client
}

func (q *OutboundMailQueue) EnqueueOutboundDispatch(ctx context.Context, delay time.Duration) error {
	if q == nil || q.client == nil {
		return fmt.Errorf("outbound mail queue is unavailable")
	}
	asynqTask := asynq.NewTask(TypeOutboundDispatch, nil)
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
		return fmt.Errorf("enqueue outbound mail dispatcher task: %w", err)
	}
	return nil
}

func NewOutboundMailQueue(client *asynq.Client) *OutboundMailQueue {
	return &OutboundMailQueue{client: client}
}

func (q *OutboundMailQueue) EnqueueOutboundSend(ctx context.Context, task mailapp.OutboundSendTask) (bool, error) {
	if q == nil || q.client == nil {
		return false, fmt.Errorf("outbound mail queue is unavailable")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("marshal outbound mail task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeOutboundSend, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(mailQueueName),
		asynq.Unique(outboundTaskTimeout),
		asynq.MaxRetry(outboundTaskMaxRetry),
		asynq.Timeout(outboundTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		return false, fmt.Errorf("enqueue outbound mail task: %w", err)
	}
	return true, nil
}
