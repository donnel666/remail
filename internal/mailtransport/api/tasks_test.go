package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

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

type microsoftAliasRunningRetryStoreStub struct {
	mailapp.MicrosoftAliasScheduleStore
	cleanupCalls int
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
