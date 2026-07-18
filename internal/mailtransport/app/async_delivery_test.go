package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type outboundQueueStub struct {
	err        error
	duplicate  bool
	tasks      []OutboundSendTask
	dispatches []time.Duration
}

func (q *outboundQueueStub) EnqueueOutboundSend(_ context.Context, task OutboundSendTask) (bool, error) {
	if q.err != nil {
		return false, q.err
	}
	if q.duplicate {
		return false, nil
	}
	q.tasks = append(q.tasks, task)
	return true, nil
}

func (q *outboundQueueStub) EnqueueOutboundDispatch(_ context.Context, delay time.Duration) error {
	if q.err != nil {
		return q.err
	}
	q.dispatches = append(q.dispatches, delay)
	return nil
}

func TestAsyncDeliveryServiceEnqueuesWithoutCallingSender(t *testing.T) {
	store := newMemoryOutboundMailStore()
	queue := &outboundQueueStub{}
	service := NewAsyncDeliveryService(store, queue, nil, "no-reply@example.com")
	service.now = fixedClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))

	msg := VerificationCodeMessage("user@example.com", "123456")
	require.NoError(t, service.Send(context.Background(), msg))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusSending, record.Status)
	assert.Equal(t, "no-reply@example.com", record.Sender)
	assert.NotEmpty(t, record.RequestHash)
	require.Len(t, queue.tasks, 1)
	assert.Equal(t, msg.IdempotencyKey, queue.tasks[0].IdempotencyKey)
	assert.Equal(t, uint64(1), queue.tasks[0].SendGeneration)
}

func TestAsyncDeliveryServiceKeepsPendingAndLogsWhenQueueUnavailable(t *testing.T) {
	store := newMemoryOutboundMailStore()
	queue := &outboundQueueStub{err: errors.New("redis unavailable")}
	logs := &systemLogStub{}
	service := NewAsyncDeliveryService(store, queue, logs, "no-reply@example.com")

	msg := VerificationCodeMessage("user@example.com", "123456")
	err := service.Send(context.Background(), msg)
	require.NoError(t, err)

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Empty(t, record.FailureReason)
	require.Len(t, logs.logs, 1)
	assert.Equal(t, "mail.outbound_enqueue_failed", logs.logs[0].EventType)
	assert.NotContains(t, logs.logs[0].Detail, "user@example.com")
}

func TestAsyncDeliveryServiceDuplicateEnqueueLeavesPending(t *testing.T) {
	store := newMemoryOutboundMailStore()
	service := NewAsyncDeliveryService(store, &outboundQueueStub{duplicate: true}, nil, "no-reply@example.com")

	msg := VerificationCodeMessage("user@example.com", "123456")
	require.NoError(t, service.Send(context.Background(), msg))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Equal(t, uint64(1), record.SendGeneration)
}

func TestAsyncDeliveryServiceDispatchesPendingMails(t *testing.T) {
	store := newMemoryOutboundMailStore()
	queue := &outboundQueueStub{}
	service := NewAsyncDeliveryService(store, queue, nil, "no-reply@example.com")
	service.now = fixedClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))

	msg := VerificationCodeMessage("user@example.com", "123456")
	mail := domain.NewOutboundMail(msg, service.now())
	store.put(mail)

	result, err := service.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Attempted)
	assert.Equal(t, 1, result.Queued)
	require.Len(t, queue.tasks, 1)
	assert.Equal(t, msg.IdempotencyKey, queue.tasks[0].IdempotencyKey)
	assert.Equal(t, domain.OutboundStatusSending, store.get(msg.IdempotencyKey).Status)
}

func TestAsyncDeliveryServiceRejectsSameIdempotencyKeyWithDifferentMessage(t *testing.T) {
	store := newMemoryOutboundMailStore()
	queue := &outboundQueueStub{}
	service := NewAsyncDeliveryService(store, queue, nil, "no-reply@example.com")

	first := VerificationCodeMessage("user@example.com", "123456")
	first.IdempotencyKey = "fixed-key"
	second := VerificationCodeMessage("user@example.com", "654321")
	second.IdempotencyKey = "fixed-key"

	require.NoError(t, service.Send(context.Background(), first))
	err := service.Send(context.Background(), second)

	require.ErrorIs(t, err, domain.ErrOutboundIdempotencyConflict)
	require.Len(t, queue.tasks, 1)
	record := store.get("fixed-key")
	require.NotNil(t, record)
	assert.Contains(t, record.TextBody, "123456")
}

func TestOutboundSendUseCaseMarksSent(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{}
	useCase := NewOutboundSendUseCase(store, sender, nil)
	useCase.now = fixedClock(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC))

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, useCase.now()))
	require.NoError(t, err)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, false))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusSent, record.Status)
	assert.Equal(t, 0, record.Retries)
	assert.Equal(t, 1, sender.calls)
	require.Len(t, sender.messages, 1)
	assert.Equal(t, msg.HTMLBody, sender.messages[0].HTMLBody)
}

func TestOutboundSendUseCaseReturnsPendingBeforeFinalAttempt(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{err: &OutboundSendFailure{SafeMessage: "SMTP server temporarily rejected the message.", Retryable: true, Cause: errors.New("smtp 451")}}
	logs := &systemLogStub{}
	useCase := NewOutboundSendUseCase(store, sender, logs)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	err = useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, false)
	require.NoError(t, err)

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Equal(t, 1, record.Retries)
	assert.Equal(t, uint64(2), record.SendGeneration)
	assert.Equal(t, "SMTP server temporarily rejected the message.", record.FailureReason)
	require.Len(t, logs.logs, 1)
	assert.Equal(t, "mail.outbound_retry", logs.logs[0].EventType)
}

func TestOutboundSendUseCaseMarksFailedOnThirdBusinessAttempt(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{err: &OutboundSendFailure{SafeMessage: "SMTP server temporarily rejected the message.", Retryable: true, Cause: errors.New("smtp 451")}}
	logs := &systemLogStub{}
	useCase := NewOutboundSendUseCase(store, sender, logs)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	for generation := uint64(1); generation <= 3; generation++ {
		err = useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: generation}, generation == 3)
		require.NoError(t, err)
	}

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusFailed, record.Status)
	assert.Equal(t, 3, record.Retries)
	assert.Equal(t, "SMTP server temporarily rejected the message.", record.FailureReason)
	require.Len(t, logs.logs, 3)
	assert.Equal(t, "mail.outbound_failed", logs.logs[2].EventType)
}

func TestOutboundSendUseCaseSuccessResetsBusinessAttempts(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{err: &OutboundSendFailure{SafeMessage: "SMTP server temporarily rejected the message.", Retryable: true, Cause: errors.New("smtp 451")}}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, false))
	sender.err = nil
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 2}, false))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusSent, record.Status)
	assert.Zero(t, record.Retries)
}

func TestOutboundSendUseCaseStaleGenerationCannotSend(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	mail := domain.NewOutboundMail(msg, time.Now().UTC())
	mail.SendGeneration = 2
	store.put(mail)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, false))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Zero(t, sender.calls)
}

func TestOutboundSendUseCaseContinuesWhenDispatcherWinsActivationRace(t *testing.T) {
	store := newMemoryOutboundMailStore()
	store.activateRace = true
	sender := &senderStub{}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, false))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusSent, record.Status)
	assert.Equal(t, 1, sender.calls)
}

func TestOutboundSendUseCaseContextCancellationDoesNotCountBusinessAttempt(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{err: context.Canceled}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, useCase.Process(ctx, OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, true))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Equal(t, uint64(2), record.SendGeneration)
	assert.Zero(t, record.Retries)
}

func TestOutboundSendUseCaseCompletionPersistenceExhaustionReleasesPending(t *testing.T) {
	store := newMemoryOutboundMailStore()
	store.markSentErr = errors.New("database unavailable")
	sender := &senderStub{}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, true))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Equal(t, uint64(2), record.SendGeneration)
	assert.Zero(t, record.Retries)
}

func TestOutboundSendUseCaseFailurePersistenceExhaustionDoesNotCountBusinessAttempt(t *testing.T) {
	store := newMemoryOutboundMailStore()
	store.recordErr = errors.New("database unavailable")
	sender := &senderStub{err: &OutboundSendFailure{SafeMessage: "SMTP server temporarily rejected the message.", Retryable: true, Cause: errors.New("smtp 451")}}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, true))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Equal(t, uint64(2), record.SendGeneration)
	assert.Zero(t, record.Retries)
}

func TestOutboundSendUseCaseUnknownSenderErrorIsInfrastructureFailure(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{err: errors.New("dial timeout")}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, true))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusPending, record.Status)
	assert.Equal(t, uint64(2), record.SendGeneration)
	assert.Zero(t, record.Retries)
}

func TestOutboundSendUseCaseNonRetryableBusinessFailureIsTerminal(t *testing.T) {
	store := newMemoryOutboundMailStore()
	sender := &senderStub{err: &OutboundSendFailure{SafeMessage: "SMTP server rejected the recipient.", Cause: errors.New("smtp 550")}}
	useCase := NewOutboundSendUseCase(store, sender, nil)

	msg := VerificationCodeMessage("user@example.com", "123456")
	_, _, err := store.Reserve(context.Background(), domain.NewOutboundMail(msg, time.Now().UTC()))
	require.NoError(t, err)
	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{IdempotencyKey: msg.IdempotencyKey, SendGeneration: 1}, false))

	record := store.get(msg.IdempotencyKey)
	require.NotNil(t, record)
	assert.Equal(t, domain.OutboundStatusFailed, record.Status)
	assert.Equal(t, 1, record.Retries)
}
