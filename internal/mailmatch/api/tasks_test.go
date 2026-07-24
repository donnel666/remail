package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type pickupFallbackTaskRepo struct {
	mailmatchapp.Repository
	valid mailmatchapp.OrderScope
}

func (r *pickupFallbackTaskRepo) LoadOrderScopeForServiceToken(_ context.Context, orderNo string) (*mailmatchapp.OrderScope, error) {
	if orderNo != r.valid.OrderNo {
		return nil, domain.ErrOrderUnavailable
	}
	scope := r.valid
	return &scope, nil
}

func (*pickupFallbackTaskRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*pickupFallbackTaskRepo) UpsertMessages(_ context.Context, messages []domain.Message) ([]domain.Message, error) {
	return messages, nil
}

type pickupFallbackTaskTransport struct {
	mu    sync.Mutex
	calls []string
}

func (t *pickupFallbackTaskTransport) FetchMicrosoftMessages(_ context.Context, req mailmatchapp.FetchMessagesRequest) (*mailmatchapp.FetchMessagesResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, req.Scope.OrderNo)
	return &mailmatchapp.FetchMessagesResult{}, nil
}

type pickupFallbackTaskState struct {
	mu       sync.Mutex
	acquired int
	released int
}

func (s *pickupFallbackTaskState) Acquire(context.Context, uint, string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquired++
	return true, nil
}

func (*pickupFallbackTaskState) Owns(context.Context, uint, string) (bool, error) { return true, nil }
func (*pickupFallbackTaskState) Extend(context.Context, uint, string, time.Duration) (bool, error) {
	return true, nil
}

func (s *pickupFallbackTaskState) Release(context.Context, uint, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released++
	return nil
}

func TestProjectHistoryCapacityIsLimitedToFourWorkers(t *testing.T) {
	releases := make([]func(), 0, projectHistoryConcurrency)
	for range projectHistoryConcurrency {
		release, admitted := acquireProjectHistoryCapacity(nil)
		require.True(t, admitted)
		releases = append(releases, release)
	}
	_, admitted := acquireProjectHistoryCapacity(nil)
	require.False(t, admitted)
	for _, release := range releases {
		release()
	}
}

func TestProjectHistoryCapacityUpdatesAtRuntime(t *testing.T) {
	defer runtimeconfig.Delete("project_history_concurrency")
	runtimeconfig.Set("project_history_concurrency", "2")

	first, admitted := acquireProjectHistoryCapacity(nil)
	require.True(t, admitted)
	second, admitted := acquireProjectHistoryCapacity(nil)
	require.True(t, admitted)
	_, admitted = acquireProjectHistoryCapacity(nil)
	require.False(t, admitted)
	first()
	second()
}

func TestPickupRequestTaskFallsBackToSecondOrderAndFetchesResourceOnce(t *testing.T) {
	repo := &pickupFallbackTaskRepo{valid: mailmatchapp.OrderScope{
		OrderNo: "ORDER-VALID", OrderStatus: "active", ServiceMode: "purchase",
		AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 12,
		EmailResourceID: 98, Recipient: "alias@example.com",
	}}
	transport := &pickupFallbackTaskTransport{}
	state := &pickupFallbackTaskState{}
	useCase := mailmatchapp.NewUseCase(repo, nil, transport, nil)
	useCase.SetPickupFetchStatePort(state)
	payload, err := json.Marshal(mailmatchapp.PickupRequestFetchTask{
		RequestedAt: time.Now().UTC(),
		Scopes: []mailmatchapp.PickupRequestFetchScope{{
			OrderNo: "ORDER-EXPIRED", OrderNos: []string{"ORDER-EXPIRED", "ORDER-VALID"}, EmailResourceID: 98,
		}},
	})
	require.NoError(t, err)

	err = processPickupRequestFetchTask(
		context.Background(),
		asynq.NewTask(mailmatchinfra.TypeMailmatchPickupRequestFetch, payload),
		&Module{UseCase: useCase},
	)

	require.NoError(t, err)
	transport.mu.Lock()
	require.Equal(t, []string{"ORDER-VALID"}, transport.calls)
	transport.mu.Unlock()
	state.mu.Lock()
	require.Equal(t, 1, state.acquired)
	require.Equal(t, 1, state.released)
	state.mu.Unlock()
}

func TestPickupRequestTaskResult(t *testing.T) {
	tests := []struct {
		name    string
		outcome mailmatchapp.PickupRequestFetchOutcome
		err     error
		want    string
	}{
		{name: "succeeded", outcome: mailmatchapp.PickupRequestFetchOutcome{Succeeded: 1}, want: "succeeded"},
		{name: "no work", outcome: mailmatchapp.PickupRequestFetchOutcome{NoWork: 1}, want: "no_work"},
		{name: "expired", outcome: mailmatchapp.PickupRequestFetchOutcome{Expired: 1}, want: "expired"},
		{name: "failed", outcome: mailmatchapp.PickupRequestFetchOutcome{Failed: 1}, err: errors.New("failed"), want: "system_failed"},
		{name: "partial failure", outcome: mailmatchapp.PickupRequestFetchOutcome{Succeeded: 1, Failed: 1}, err: errors.New("failed"), want: "partial"},
		{name: "partial expiry", outcome: mailmatchapp.PickupRequestFetchOutcome{Succeeded: 1, Expired: 1}, want: "partial"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, pickupRequestTaskResult(test.outcome, test.err))
		})
	}
}
