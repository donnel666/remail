package infra

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	TypeOutboundSend = "mailtransport:outbound_send"
	// TypeOutboundDispatch is retained only so deployments can drain legacy tasks.
	TypeOutboundDispatch = "mailtransport:outbound_dispatch"
	mailQueueName        = platform.QueueMailtransport
	outboundTaskTimeout  = 3 * time.Minute
	outboundPayloadTTL   = 5 * time.Minute
	outboundCleanupLimit = 5 * time.Second
	dispatchTaskTimeout  = 30 * time.Second
)

type OutboundMailQueue struct {
	client *asynq.Client
	redis  redis.UniversalClient
}

func NewOutboundMailQueue(client *asynq.Client, redisClient redis.UniversalClient) *OutboundMailQueue {
	return &OutboundMailQueue{client: client, redis: redisClient}
}

func (q *OutboundMailQueue) EnqueueOutboundSend(ctx context.Context, task mailapp.OutboundSendTask) (bool, error) {
	if q == nil || q.client == nil || q.redis == nil {
		return false, fmt.Errorf("outbound mail queue is unavailable")
	}
	messagePayload, err := json.Marshal(task.Message)
	if err != nil {
		return false, fmt.Errorf("marshal outbound mail payload: %w", err)
	}
	id := outboundPayloadID(task.Message.IdempotencyKey)
	if id == "" {
		return false, fmt.Errorf("outbound mail identity is required")
	}
	key := outboundPayloadKey(id)
	created, err := q.redis.SetNX(ctx, key, messagePayload, outboundPayloadTTL).Result()
	if err != nil {
		return false, fmt.Errorf("store outbound mail payload: %w", err)
	}
	if !created {
		stored, getErr := q.redis.Get(ctx, key).Bytes()
		if getErr != nil {
			return false, fmt.Errorf("read outbound mail payload: %w", getErr)
		}
		if !bytes.Equal(stored, messagePayload) {
			return false, domain.ErrOutboundIdempotencyConflict
		}
	}
	payload, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		if created {
			q.deleteAfterEnqueueFailure(ctx, id)
		}
		return false, fmt.Errorf("marshal outbound mail task: %w", err)
	}
	asynqTask := asynq.NewTask(TypeOutboundSend, payload)
	_, err = q.client.EnqueueContext(
		ctx,
		asynqTask,
		asynq.Queue(mailQueueName),
		asynq.Unique(outboundPayloadTTL),
		asynq.MaxRetry(0),
		asynq.Timeout(outboundTaskTimeout),
		asynq.Retention(0),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrDuplicateTask) {
			return false, nil
		}
		if created {
			q.deleteAfterEnqueueFailure(ctx, id)
		}
		return false, fmt.Errorf("enqueue outbound mail task: %w", err)
	}
	return true, nil
}

func (q *OutboundMailQueue) LoadOutboundSend(ctx context.Context, id string) (mailapp.OutboundSendTask, bool, error) {
	if q == nil || q.redis == nil || strings.TrimSpace(id) == "" {
		return mailapp.OutboundSendTask{}, false, nil
	}
	payload, err := q.redis.Get(ctx, outboundPayloadKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return mailapp.OutboundSendTask{}, false, nil
	}
	if err != nil {
		return mailapp.OutboundSendTask{}, false, fmt.Errorf("load outbound mail payload: %w", err)
	}
	var message domain.OutboundMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return mailapp.OutboundSendTask{}, false, fmt.Errorf("decode outbound mail payload: %w", err)
	}
	return mailapp.OutboundSendTask{ID: strings.TrimSpace(id), Message: message}, true, nil
}

func (q *OutboundMailQueue) DeleteOutboundSend(ctx context.Context, id string) error {
	if q == nil || q.redis == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	if err := q.redis.Del(ctx, outboundPayloadKey(id)).Err(); err != nil {
		return fmt.Errorf("delete outbound mail payload: %w", err)
	}
	return nil
}

func (q *OutboundMailQueue) deleteAfterEnqueueFailure(ctx context.Context, id string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outboundCleanupLimit)
	defer cancel()
	_ = q.DeleteOutboundSend(cleanupCtx, id)
}

func outboundPayloadID(idempotencyKey string) string {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(idempotencyKey))
	return fmt.Sprintf("%x", digest)
}

func outboundPayloadKey(id string) string {
	return "remail:mailtransport:outbound:" + strings.TrimSpace(id)
}
