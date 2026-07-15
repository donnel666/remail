package api

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type mailBackgroundExecutionGateStub struct {
	admitted bool
	released atomic.Bool
}

func (s *mailBackgroundExecutionGateStub) TryAcquire() (func(), bool) {
	return func() { s.released.Store(true) }, s.admitted
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
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
		RequestID:     "request-42",
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
		JobID:         91,
		ResourceID:    42,
		DispatchToken: "dispatch-token",
		RequestID:     "request-42",
	})
	require.NoError(t, err)

	err = mux.ProcessTask(context.Background(), asynq.NewTask(mailinfra.TypeMicrosoftTokenRefresh, encoded))

	require.NoError(t, err)
	require.True(t, gate.released.Load())
}
