package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const TypeRechargeReconcile = "billing:recharge_reconcile"

type RechargeQueue struct {
	client *asynq.Client
}

func NewRechargeQueue(client *asynq.Client) *RechargeQueue {
	return &RechargeQueue{client: client}
}

func (queue *RechargeQueue) Enqueue(ctx context.Context, task billingapp.RechargeTask) error {
	if queue == nil || queue.client == nil {
		return fmt.Errorf("recharge queue is unavailable")
	}
	if task.RechargeNo == "" {
		return fmt.Errorf("recharge task identity is required")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal recharge task: %w", err)
	}
	_, err = queue.client.EnqueueContext(
		ctx,
		asynq.NewTask(TypeRechargeReconcile, payload),
		asynq.Queue(platform.QueuePaymentReconcile),
		asynq.Unique(domain.RechargeReconciliationWindow()+domain.RechargeQueryLease),
		asynq.MaxRetry(5),
		asynq.Timeout(domain.RechargeQueryLease),
		asynq.Retention(0),
	)
	if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
		return fmt.Errorf("enqueue recharge reconciliation: %w", err)
	}
	return nil
}
