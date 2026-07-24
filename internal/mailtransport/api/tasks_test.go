package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type mailBackgroundExecutionGateStub struct {
	admitted bool
	released atomic.Bool
}

func (s *mailBackgroundExecutionGateStub) TryAcquire() (func(), bool) {
	return func() { s.released.Store(true) }, s.admitted
}

type microsoftAliasRunningRetryStoreStub struct {
	mailapp.MicrosoftAliasScheduleStore
	cleanupCalls int
}

type failingOutboundSenderStub struct {
	calls int
	panic bool
}

func (s *failingOutboundSenderStub) Send(context.Context, maildomain.OutboundMessage) error {
	s.calls++
	if s.panic {
		panic("smtp panic")
	}
	return &mailapp.OutboundSendFailure{SafeMessage: "SMTP server rejected the message.", Cause: errors.New("smtp 550")}
}

func TestOutboundSMTPFailureCompletesWithoutAsynqRetry(t *testing.T) {
	sender := &failingOutboundSenderStub{}
	module := &MailTransportModule{OutboundSendUseCase: mailapp.NewOutboundSendUseCase(sender)}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	payload, err := json.Marshal(mailapp.OutboundSendTask{Message: mailapp.VerificationCodeMessage("user@example.com", "123456")})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeOutboundSend, payload))

	require.NoError(t, err)
	require.Equal(t, 1, sender.calls)
}

func TestOutboundSMTPFailureDeletesTemporaryRedisPayload(t *testing.T) {
	sender := &failingOutboundSenderStub{}
	mux, task, queue, id := queuedOutboundTaskForHandler(t, sender)

	err := mux.ProcessTask(context.Background(), task)

	require.NoError(t, err)
	require.Equal(t, 1, sender.calls)
	_, found, loadErr := queue.LoadOutboundSend(context.Background(), id)
	require.NoError(t, loadErr)
	require.False(t, found)
}

func TestOutboundSMTPPanicDeletesPayloadAndRevokesTask(t *testing.T) {
	sender := &failingOutboundSenderStub{panic: true}
	mux, task, queue, id := queuedOutboundTaskForHandler(t, sender)

	err := mux.ProcessTask(context.Background(), task)

	require.ErrorIs(t, err, asynq.RevokeTask)
	_, found, loadErr := queue.LoadOutboundSend(context.Background(), id)
	require.NoError(t, loadErr)
	require.False(t, found)
}

func queuedOutboundTaskForHandler(t *testing.T, sender mailapp.SenderPort) (*asynq.ServeMux, *asynq.Task, *mailinfra.OutboundMailQueue, string) {
	t.Helper()
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	asynqOptions := asynq.RedisClientOpt{Addr: server.Addr()}
	asynqClient := asynq.NewClient(asynqOptions)
	inspector := asynq.NewInspector(asynqOptions)
	t.Cleanup(func() {
		require.NoError(t, inspector.Close())
		require.NoError(t, asynqClient.Close())
		require.NoError(t, redisClient.Close())
	})
	queue := mailinfra.NewOutboundMailQueue(asynqClient, redisClient)
	accepted, err := queue.EnqueueOutboundSend(context.Background(), mailapp.OutboundSendTask{Message: mailapp.VerificationCodeMessage("user@example.com", "123456")})
	require.NoError(t, err)
	require.True(t, accepted)
	pending, err := inspector.ListPendingTasks(platform.QueueMailtransport)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var payload mailapp.OutboundSendTask
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, &MailTransportModule{
		OutboundSendUseCase: mailapp.NewOutboundSendUseCase(sender),
		OutboundQueue:       queue,
	})
	return mux, asynq.NewTask(mailinfra.TypeOutboundSend, pending[0].Payload), queue, payload.ID
}

func (*microsoftAliasRunningRetryStoreStub) MarkQueued(context.Context, mailapp.MicrosoftAliasTask, time.Time) (bool, error) {
	return false, nil
}

func (*microsoftAliasRunningRetryStoreStub) Claim(context.Context, mailapp.MicrosoftAliasTask, time.Time) (*mailapp.MicrosoftAliasAccount, bool, error) {
	return nil, false, errors.New("microsoft alias generation is still running")
}

func (s *microsoftAliasRunningRetryStoreStub) MarkDispatchFailed(context.Context, mailapp.MicrosoftAliasTask, time.Time, string) error {
	s.cleanupCalls++
	if s.cleanupCalls == 1 {
		return errors.New("database unavailable")
	}
	return nil
}

func TestMicrosoftTokenRefreshAdmissionDenialDefersInAsynqWithoutDatabaseMutation(t *testing.T) {
	gate := &mailBackgroundExecutionGateStub{}
	repo := &adminTokenRefreshRepoStub{}
	module := &MailTransportModule{
		TokenRefresh:        mailapp.NewMicrosoftTokenRefreshService(repo, adminTokenRefreshQueueStub{}, nil),
		BackgroundExecution: gate,
	}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	payload := mailapp.MicrosoftTokenRefreshTask{
		ResourceID:                 42,
		Generation:                 3,
		ExpectedCredentialRevision: 7,
		RequestID:                  "request-42",
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeMicrosoftTokenRefresh, encoded))

	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.Zero(t, repo.releaseCalls)
	require.False(t, gate.released.Load(), "a denied task never owns a permit")
}

func TestMicrosoftTokenRefreshAdmissionPermitIsReleasedAfterNoopClaim(t *testing.T) {
	gate := &mailBackgroundExecutionGateStub{admitted: true}
	repo := &adminTokenRefreshRepoStub{}
	module := &MailTransportModule{
		TokenRefresh:        mailapp.NewMicrosoftTokenRefreshService(repo, adminTokenRefreshQueueStub{}, nil),
		BackgroundExecution: gate,
	}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	encoded, err := json.Marshal(mailapp.MicrosoftTokenRefreshTask{
		ResourceID:                 42,
		Generation:                 3,
		ExpectedCredentialRevision: 7,
		RequestID:                  "request-42",
	})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeMicrosoftTokenRefresh, encoded))

	require.NoError(t, err)
	require.True(t, gate.released.Load())
}

func TestMicrosoftAliasRunningRetryRepeatsCleanupUntilItSucceeds(t *testing.T) {
	gate := &mailBackgroundExecutionGateStub{admitted: true}
	store := &microsoftAliasRunningRetryStoreStub{}
	module := &MailTransportModule{
		MicrosoftAliases:    mailapp.NewMicrosoftAliasService(store, nil, nil),
		AliasDispatch:       store,
		BackgroundExecution: gate,
	}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	encoded, err := json.Marshal(mailapp.MicrosoftAliasTask{ResourceID: 42, Generation: 7})
	require.NoError(t, err)
	task := asynq.NewTask(mailinfra.TypeMicrosoftAlias, encoded)

	err = mux.ProcessTask(context.Background(), task)
	require.Error(t, err, "a failed cleanup must keep the same Asynq task retryable")
	require.Equal(t, 1, store.cleanupCalls)

	err = mux.ProcessTask(context.Background(), task)
	require.NoError(t, err, "the retry completes only after running state cleanup succeeds")
	require.Equal(t, 2, store.cleanupCalls)
	require.True(t, gate.released.Load())
}
