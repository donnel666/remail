package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
)

type outboundQueueStub struct {
	err       error
	duplicate bool
	tasks     []OutboundSendTask
}

type senderFunc func(context.Context, domain.OutboundMessage) error

func (send senderFunc) Send(ctx context.Context, message domain.OutboundMessage) error {
	return send(ctx, message)
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

func TestAsyncDeliveryServiceQueuesCompleteTemporaryMessage(t *testing.T) {
	queue := &outboundQueueStub{}
	service := NewAsyncDeliveryService(queue, "no-reply@example.com")
	message := VerificationCodeMessage("user@example.com", "123456")

	require.NoError(t, service.Send(context.Background(), message))
	require.Len(t, queue.tasks, 1)
	require.Equal(t, "no-reply@example.com", queue.tasks[0].Message.From)
	require.Equal(t, message.To, queue.tasks[0].Message.To)
	require.Equal(t, message.HTMLBody, queue.tasks[0].Message.HTMLBody)
}

func TestAsyncDeliveryServiceReportsRedisFailure(t *testing.T) {
	service := NewAsyncDeliveryService(&outboundQueueStub{err: errors.New("redis unavailable")}, "no-reply@example.com")

	err := service.Send(context.Background(), VerificationCodeMessage("user@example.com", "123456"))
	require.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
}

func TestOutboundSendUseCaseRetriesInfrastructureFailureThreeTimes(t *testing.T) {
	sender := &senderStub{err: errors.New("smtp unavailable")}
	useCase := NewOutboundSendUseCase(sender)
	useCase.retryDelay = func(int) time.Duration { return 0 }
	task := OutboundSendTask{Message: VerificationCodeMessage("user@example.com", "123456")}

	err := useCase.Process(context.Background(), task)
	require.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
	require.Equal(t, 4, sender.calls)
}

func TestOutboundSendUseCaseDoesNotRetryPermanentSMTPFailure(t *testing.T) {
	sender := &senderStub{err: &OutboundSendFailure{SafeMessage: "SMTP server rejected the message.", Cause: errors.New("smtp 550")}}
	useCase := NewOutboundSendUseCase(sender)
	useCase.retryDelay = func(int) time.Duration { return 0 }

	err := useCase.Process(context.Background(), OutboundSendTask{Message: VerificationCodeMessage("user@example.com", "123456")})

	require.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
	require.Equal(t, 1, sender.calls)
}

func TestOutboundSendUseCaseRetriesTemporarySMTPFailureUntilSuccess(t *testing.T) {
	calls := 0
	useCase := NewOutboundSendUseCase(senderFunc(func(context.Context, domain.OutboundMessage) error {
		calls++
		if calls < 3 {
			return &OutboundSendFailure{SafeMessage: "SMTP server temporarily rejected the message.", Retryable: true, Cause: errors.New("smtp 451")}
		}
		return nil
	}))
	useCase.retryDelay = func(int) time.Duration { return 0 }

	require.NoError(t, useCase.Process(context.Background(), OutboundSendTask{Message: VerificationCodeMessage("user@example.com", "123456")}))
	require.Equal(t, 3, calls)
}

func TestOutboundSendUseCaseStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	useCase := NewOutboundSendUseCase(senderFunc(func(context.Context, domain.OutboundMessage) error {
		calls++
		cancel()
		return errors.New("smtp unavailable")
	}))
	useCase.retryDelay = func(int) time.Duration { return time.Hour }

	err := useCase.Process(ctx, OutboundSendTask{Message: VerificationCodeMessage("user@example.com", "123456")})

	require.ErrorIs(t, err, domain.ErrDeliveryUnavailable)
	require.Equal(t, 1, calls)
}
