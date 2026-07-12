package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type backgroundDispatchStub struct {
	admitted bool
	queue    string
	released bool
}

func (s *backgroundDispatchStub) AcquireDispatchBudget(context.Context, string, int, int) (int, func()) {
	return 0, func() {}
}

func (s *backgroundDispatchStub) TryAcquireExecution(_ context.Context, queue string) (bool, func()) {
	s.queue = queue
	return s.admitted, func() { s.released = true }
}

type aliasDispatchReleaserStub struct {
	called    bool
	task      mailapp.MicrosoftAliasTask
	nextRunAt time.Time
}

func (s *aliasDispatchReleaserStub) MarkDispatchFailed(_ context.Context, task mailapp.MicrosoftAliasTask, nextRunAt time.Time, _ string) error {
	s.called = true
	s.task = task
	s.nextRunAt = nextRunAt
	return nil
}

func TestMicrosoftAliasTaskAdmissionDenialReturnsScheduleToPending(t *testing.T) {
	gate := &backgroundDispatchStub{}
	releaser := &aliasDispatchReleaserStub{}
	module := &MailTransportModule{
		MicrosoftAliases:   mailapp.NewMicrosoftAliasService(nil, nil, nil),
		BackgroundDispatch: gate,
		AliasDispatch:      releaser,
	}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	payload := mailapp.MicrosoftAliasTask{
		ResourceID:    42,
		DispatchToken: "0123456789abcdef0123456789abcdef",
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	startedAt := time.Now().UTC()

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeMicrosoftAlias, encoded))

	require.NoError(t, err)
	require.Equal(t, mailinfra.MicrosoftAliasQueueName, gate.queue)
	require.True(t, releaser.called)
	require.Equal(t, payload, releaser.task)
	require.WithinDuration(t, startedAt.Add(microsoftAliasAdmissionRetryDelay), releaser.nextRunAt, time.Second)
	require.False(t, gate.released)
}

func TestMicrosoftAliasTaskReleasesAdmissionSlotAfterProcessingFailure(t *testing.T) {
	gate := &backgroundDispatchStub{admitted: true}
	module := &MailTransportModule{
		MicrosoftAliases:   mailapp.NewMicrosoftAliasService(nil, nil, nil),
		BackgroundDispatch: gate,
	}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	encoded, err := json.Marshal(mailapp.MicrosoftAliasTask{
		ResourceID:    42,
		DispatchToken: "0123456789abcdef0123456789abcdef",
	})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeMicrosoftAlias, encoded))

	require.Error(t, err)
	require.True(t, gate.released)
}

func TestMicrosoftTokenRefreshTaskAdmissionDenialReleasesDurableDispatch(t *testing.T) {
	gate := &backgroundDispatchStub{}
	repo := &adminTokenRefreshRepoStub{}
	module := &MailTransportModule{
		TokenRefresh:       mailapp.NewMicrosoftTokenRefreshService(repo, adminTokenRefreshQueueStub{}, nil),
		BackgroundDispatch: gate,
	}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	payload := mailapp.MicrosoftTokenRefreshTask{
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
		RequestID:     "request-42",
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeMicrosoftTokenRefresh, encoded))

	require.NoError(t, err)
	require.Equal(t, mailinfra.MicrosoftTokenRefreshQueueName, gate.queue)
	require.Equal(t, 1, repo.releaseCalls)
	require.Equal(t, uint64(91), repo.releasedID)
	require.Equal(t, "dispatch-token", repo.releasedToken)
	require.False(t, gate.released)
}

func TestMicrosoftTokenRefreshTaskReleasesAdmissionSlotAfterProcessing(t *testing.T) {
	gate := &backgroundDispatchStub{admitted: true}
	repo := &adminTokenRefreshRepoStub{}
	module := &MailTransportModule{
		TokenRefresh:       mailapp.NewMicrosoftTokenRefreshService(repo, adminTokenRefreshQueueStub{}, nil),
		BackgroundDispatch: gate,
	}
	mux := asynq.NewServeMux()
	RegisterMailTransportTaskHandlers(mux, module)
	encoded, err := json.Marshal(mailapp.MicrosoftTokenRefreshTask{
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
		RequestID:     "request-42",
	})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeMicrosoftTokenRefresh, encoded))

	require.NoError(t, err)
	require.True(t, gate.released)
}
