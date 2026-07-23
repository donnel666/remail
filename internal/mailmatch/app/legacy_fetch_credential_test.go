package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/stretchr/testify/require"
)

type legacyFetchRepoStub struct {
	Repository
	scope OrderScope
}

func (s *legacyFetchRepoStub) LoadOrderScopeForServiceToken(context.Context, string) (*OrderScope, error) {
	scope := s.scope
	return &scope, nil
}

func (*legacyFetchRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*legacyFetchRepoStub) UpsertMessages(_ context.Context, messages []domain.Message) ([]domain.Message, error) {
	return messages, nil
}

type legacyFetchTransportStub struct {
	result FetchMessagesResult
	err    error
}

type pickupRequestRepoStub struct {
	Repository
	scopes map[string]OrderScope
}

func (s *pickupRequestRepoStub) LoadOrderScopeForServiceToken(_ context.Context, orderNo string) (*OrderScope, error) {
	scope, ok := s.scopes[orderNo]
	if !ok {
		return nil, domain.ErrOrderNotFound
	}
	return &scope, nil
}

func (*pickupRequestRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*pickupRequestRepoStub) UpsertMessages(_ context.Context, messages []domain.Message) ([]domain.Message, error) {
	return messages, nil
}

type pickupRequestTransportStub struct {
	calls []string
}

func (s *pickupRequestTransportStub) FetchMicrosoftMessages(_ context.Context, req FetchMessagesRequest) (*FetchMessagesResult, error) {
	s.calls = append(s.calls, req.Scope.OrderNo)
	if req.Scope.OrderNo == "ORDER-FAIL" {
		return nil, errors.New("provider unavailable")
	}
	return &FetchMessagesResult{}, nil
}

func (s legacyFetchTransportStub) FetchMicrosoftMessages(context.Context, FetchMessagesRequest) (*FetchMessagesResult, error) {
	result := s.result
	return &result, s.err
}

type legacyFetchCredentialStub struct {
	coreapp.MicrosoftCredentialPort
	update coreapp.MicrosoftFetchRefreshTokenRotation
	err    error
}

type pickupFetchLeaseSequenceStub struct {
	*pickupFetchStateStub
	mu      sync.Mutex
	results []bool
}

func (s *pickupFetchLeaseSequenceStub) Extend(context.Context, uint, string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return false, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func (s *legacyFetchCredentialStub) ApplyMicrosoftFetchRefreshToken(_ context.Context, update coreapp.MicrosoftFetchRefreshTokenRotation) error {
	s.update = update
	return s.err
}

func TestPickupFetchUsesCredentialRevisionFence(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{
		scope: OrderScope{
			OrderNo: "ORDER-5", OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 8, EmailResourceID: 91,
			Recipient: "alias@example.com", MicrosoftRT: "old-rt", CredentialRevision: 17,
		},
	}
	credentials := &legacyFetchCredentialStub{}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, nil, legacyFetchTransportStub{result: FetchMessagesResult{RefreshToken: "rotated-rt"}}, nil)
	uc.SetMicrosoftCredentialPort(credentials)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	require.NoError(t, uc.ProcessFetch(context.Background(), pickupFetchTask(91, "ORDER-5", now)))
	_, released := state.snapshot()
	require.Equal(t, 1, released)
	require.Equal(t, uint(91), credentials.update.ResourceID)
	require.Equal(t, uint64(17), credentials.update.ExpectedCredentialRevision)
	require.Equal(t, "rotated-rt", credentials.update.RefreshToken)
}

func TestPickupFetchDoesNotOverwriteNewerCredential(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{
		scope: OrderScope{
			OrderNo: "ORDER-6", OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 9, EmailResourceID: 92,
			Recipient: "alias@example.com", MicrosoftRT: "old-rt", CredentialRevision: 3,
		},
	}
	credentials := &legacyFetchCredentialStub{err: coreapp.ErrMicrosoftCredentialChanged}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, nil, legacyFetchTransportStub{result: FetchMessagesResult{RefreshToken: "stale-rotated-rt"}}, nil)
	uc.SetMicrosoftCredentialPort(credentials)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	require.NoError(t, uc.ProcessFetch(context.Background(), pickupFetchTask(92, "ORDER-6", now)))
	_, released := state.snapshot()
	require.Equal(t, 1, released)
}

func TestPickupFetchIgnoresQueuedLegacyDatabaseTask(t *testing.T) {
	uc := NewUseCase(nil, nil, nil, nil)

	err := uc.ProcessFetch(context.Background(), FetchTask{EmailResourceID: 93, Generation: 7})

	require.NoError(t, err)
}

func TestPickupFetchDropsTasksWaitingLongerThanTwoMinutes(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	state := &pickupFetchStateStub{}
	uc := NewUseCase(nil, nil, nil, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessFetch(context.Background(), FetchTask{
		OrderNo: "ORDER-STALE", EmailResourceID: 94, LeaseToken: "stale-lease",
		RequestedAt: now.Add(-pickupFetchReserveTTL - time.Nanosecond),
	})

	require.NoError(t, err)
	_, released := state.snapshot()
	require.Equal(t, 1, released)
}

func TestPickupFetchCleansUpPermanentMicrosoftFailure(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{scope: OrderScope{
		OrderNo: "ORDER-PERMANENT", OrderStatus: "active", ServiceMode: "purchase",
		AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 10, EmailResourceID: 95,
		Recipient: "alias@example.com",
	}}
	state := &pickupFetchStateStub{}
	transport := legacyFetchTransportStub{err: &MailFetchFailure{Category: "auth", Retryable: false}}
	uc := NewUseCase(repo, nil, transport, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessFetch(context.Background(), pickupFetchTask(95, "ORDER-PERMANENT", now))

	require.Error(t, err)
	_, released := state.snapshot()
	require.Equal(t, 1, released)
}

func TestPickupFetchDoesNotRotateCredentialAfterLeaseLoss(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{scope: OrderScope{
		OrderNo: "ORDER-LEASE-LOST", OrderStatus: "active", ServiceMode: "purchase",
		AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 11, EmailResourceID: 96,
		Recipient: "alias@example.com", MicrosoftRT: "old-rt", CredentialRevision: 8,
	}}
	credentials := &legacyFetchCredentialStub{}
	state := &pickupFetchLeaseSequenceStub{
		pickupFetchStateStub: &pickupFetchStateStub{},
		results:              []bool{true, false},
	}
	uc := NewUseCase(repo, nil, legacyFetchTransportStub{result: FetchMessagesResult{RefreshToken: "new-rt"}}, nil)
	uc.SetMicrosoftCredentialPort(credentials)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessFetch(context.Background(), pickupFetchTask(96, "ORDER-LEASE-LOST", now))

	require.NoError(t, err)
	require.Zero(t, credentials.update.ResourceID)
	_, released := state.snapshot()
	require.Equal(t, 1, released)
}

func TestPickupFetchReturnsRedisCleanupFailure(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{scope: OrderScope{
		OrderNo: "ORDER-CLEANUP", OrderStatus: "active", ServiceMode: "purchase",
		AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 12, EmailResourceID: 97,
		Recipient: "alias@example.com",
	}}
	cleanupErr := errors.New("redis cleanup failed")
	state := &pickupFetchStateStub{releaseErr: cleanupErr}
	uc := NewUseCase(repo, nil, legacyFetchTransportStub{}, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessFetch(context.Background(), pickupFetchTask(97, "ORDER-CLEANUP", now))

	require.ErrorIs(t, err, cleanupErr)
}

func TestPickupRequestFetchOwnsWholeRequestAndCleansLease(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	repo := &legacyFetchRepoStub{scope: OrderScope{
		OrderNo: "ORDER-REQUEST", OrderStatus: "active", ServiceMode: "purchase",
		AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 12, EmailResourceID: 98,
		Recipient: "alias@example.com",
	}}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, nil, legacyFetchTransportStub{}, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessPickupRequestFetch(context.Background(), PickupRequestFetchTask{
		RequestedAt: now,
		Scopes:      []PickupRequestFetchScope{{OrderNo: "ORDER-REQUEST", EmailResourceID: 98}},
	})

	require.NoError(t, err)
	acquired, released := state.snapshot()
	require.Equal(t, []uint{98}, acquired)
	require.Equal(t, 1, released)
}

func TestPickupRequestFetchFallsBackToNextValidOrderForResource(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	repo := &pickupRequestRepoStub{scopes: map[string]OrderScope{
		"ORDER-VALID": {
			OrderNo: "ORDER-VALID", OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 12,
			EmailResourceID: 98, Recipient: "alias@example.com",
		},
	}}
	transport := &pickupRequestTransportStub{}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, nil, transport, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessPickupRequestFetch(context.Background(), PickupRequestFetchTask{
		RequestedAt: now,
		Scopes: []PickupRequestFetchScope{{
			OrderNo: "ORDER-EXPIRED", OrderNos: []string{"ORDER-EXPIRED", "ORDER-VALID"}, EmailResourceID: 98,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"ORDER-VALID"}, transport.calls)
	acquired, released := state.snapshot()
	require.Equal(t, []uint{98}, acquired)
	require.Equal(t, 1, released)
}

func TestPickupRequestFetchExpiresBeforeAcquiringLease(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	state := &pickupFetchStateStub{}
	uc := NewUseCase(&legacyFetchRepoStub{}, nil, legacyFetchTransportStub{}, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessPickupRequestFetch(context.Background(), PickupRequestFetchTask{
		RequestedAt: now.Add(-pickupFetchReserveTTL),
		Scopes:      []PickupRequestFetchScope{{OrderNo: "ORDER-EXPIRED", EmailResourceID: 99}},
	})

	require.NoError(t, err)
	acquired, released := state.snapshot()
	require.Empty(t, acquired)
	require.Zero(t, released)
}

func TestPickupRequestFetchContinuesAfterScopeFailureAndCleansEveryLease(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	repo := &pickupRequestRepoStub{scopes: map[string]OrderScope{
		"ORDER-FAIL": {
			OrderNo: "ORDER-FAIL", OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 101, EmailResourceID: 101, Recipient: "a@example.com",
		},
		"ORDER-OK": {
			OrderNo: "ORDER-OK", OrderStatus: "active", ServiceMode: "purchase",
			AllocationType: domain.ResourceTypeMicrosoft, AllocationID: 102, EmailResourceID: 102, Recipient: "b@example.com",
		},
	}}
	transport := &pickupRequestTransportStub{}
	state := &pickupFetchStateStub{}
	uc := NewUseCase(repo, nil, transport, nil)
	uc.SetPickupFetchStatePort(state)
	uc.now = func() time.Time { return now }

	err := uc.ProcessPickupRequestFetch(context.Background(), PickupRequestFetchTask{
		RequestedAt: now,
		Scopes: []PickupRequestFetchScope{
			{OrderNo: "ORDER-FAIL", EmailResourceID: 101},
			{OrderNo: "ORDER-OK", EmailResourceID: 102},
		},
	})

	require.Error(t, err)
	require.Equal(t, []string{"ORDER-FAIL", "ORDER-OK"}, transport.calls)
	acquired, released := state.snapshot()
	require.Equal(t, []uint{101, 102}, acquired)
	require.Equal(t, 2, released)
}

func pickupFetchTask(resourceID uint, orderNo string, now time.Time) FetchTask {
	return FetchTask{
		OrderNo: orderNo, EmailResourceID: resourceID, LeaseToken: "lease",
		SinceAt: now.Add(-time.Hour), UntilAt: now, RequestedAt: now,
	}
}
