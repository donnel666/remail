package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMicrosoftAliasStore struct {
	account           *MicrosoftAliasAccount
	usage             MicrosoftAliasUsage
	postCompleteUsage *MicrosoftAliasUsage
	reserveAttempts   []MicrosoftAliasAttempt
	reserveUsage      MicrosoftAliasUsage
	claimed           bool
	completed         bool
	completedAt       time.Time
	outcomes          []MicrosoftAliasAttemptOutcome
	deferredAt        time.Time
	deferredSafe      string
	deferredFailed    bool
	paused            bool
	eligible          *bool
	eligibilityChecks int
	ensureCalls       int
	ensureResult      int64
	dispatchTasks     []MicrosoftAliasTask
	markDispatchErr   error
}

func (f *fakeMicrosoftAliasStore) EnsureSchedules(context.Context, time.Time) (int64, error) {
	f.ensureCalls++
	return f.ensureResult, nil
}

func (f *fakeMicrosoftAliasStore) FindDispatchable(context.Context, int, time.Time, time.Time, time.Time) ([]MicrosoftAliasTask, error) {
	return append([]MicrosoftAliasTask(nil), f.dispatchTasks...), nil
}

func (f *fakeMicrosoftAliasStore) Claim(_ context.Context, task MicrosoftAliasTask, _ time.Time) (*MicrosoftAliasAccount, bool, error) {
	if f.account != nil {
		f.account.ClaimToken = task.DispatchToken
	}
	return f.account, f.claimed, nil
}

func (f *fakeMicrosoftAliasStore) CheckEligibility(context.Context, uint, string) (bool, error) {
	f.eligibilityChecks++
	if f.eligible == nil {
		return true, nil
	}
	return *f.eligible, nil
}

func (f *fakeMicrosoftAliasStore) Reserve(context.Context, uint, string, []string, time.Time, time.Time, time.Time, time.Time, time.Time) ([]MicrosoftAliasAttempt, MicrosoftAliasUsage, error) {
	return append([]MicrosoftAliasAttempt(nil), f.reserveAttempts...), f.reserveUsage, nil
}

func (f *fakeMicrosoftAliasStore) Usage(context.Context, uint, time.Time, time.Time, time.Time, time.Time) (MicrosoftAliasUsage, error) {
	if f.completed && f.postCompleteUsage != nil {
		return *f.postCompleteUsage, nil
	}
	return f.usage, nil
}

func (f *fakeMicrosoftAliasStore) Complete(_ context.Context, _ uint, _ string, outcomes []MicrosoftAliasAttemptOutcome, completedAt time.Time) error {
	f.completed = true
	f.completedAt = completedAt
	f.outcomes = append([]MicrosoftAliasAttemptOutcome(nil), outcomes...)
	return nil
}

func (f *fakeMicrosoftAliasStore) Defer(_ context.Context, _ uint, _ string, nextRunAt time.Time, safeError string, failed bool) error {
	f.deferredAt = nextRunAt
	f.deferredSafe = safeError
	f.deferredFailed = failed
	return nil
}

func (f *fakeMicrosoftAliasStore) Pause(context.Context, uint, string, string) error {
	f.paused = true
	return nil
}

func (f *fakeMicrosoftAliasStore) MarkDispatchFailed(context.Context, MicrosoftAliasTask, time.Time, string) error {
	return f.markDispatchErr
}

type fakeMicrosoftAliasCreator struct {
	count         int
	reconcileOnly bool
	candidates    []string
	result        MicrosoftAliasCreationResult
}

func (f *fakeMicrosoftAliasCreator) GenerateMicrosoftAliasCandidates(count int) ([]string, error) {
	values := f.candidates
	if len(values) == 0 {
		values = []string{"first123456@outlook.com", "second123456@outlook.com"}
	}
	if len(values) > count {
		values = values[:count]
	}
	return append([]string(nil), values...), nil
}

func (f *fakeMicrosoftAliasCreator) CreateMicrosoftAliases(_ context.Context, req MicrosoftAliasCreationRequest) (MicrosoftAliasCreationResult, error) {
	f.count = len(req.Candidates)
	f.reconcileOnly = req.ReconcileOnly
	return f.result, nil
}

func microsoftAliasTestTask(resourceID uint) MicrosoftAliasTask {
	return MicrosoftAliasTask{ResourceID: resourceID, DispatchToken: "0123456789abcdef0123456789abcdef"}
}

func TestMicrosoftAliasDispatchThrottlesCompletedScheduleSweeps(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{}
	service := NewMicrosoftAliasService(store, nil, nil)
	service.now = func() time.Time { return now }

	_, err := service.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	_, err = service.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, store.ensureCalls)

	now = now.Add(microsoftAliasEnsureInterval)
	_, err = service.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 2, store.ensureCalls)
}

func TestMicrosoftAliasDispatchContinuesScheduleBackfillImmediately(t *testing.T) {
	store := &fakeMicrosoftAliasStore{ensureResult: microsoftAliasEnsureBatchHint}
	service := NewMicrosoftAliasService(store, nil, nil)
	service.now = func() time.Time {
		return time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	}

	_, err := service.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	_, err = service.DispatchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 2, store.ensureCalls)
}

func TestMicrosoftAliasDispatchReportsFailureToRestoreUnqueuedSchedule(t *testing.T) {
	store := &fakeMicrosoftAliasStore{
		dispatchTasks:   []MicrosoftAliasTask{microsoftAliasTestTask(42)},
		markDispatchErr: errors.New("database unavailable"),
	}
	service := NewMicrosoftAliasService(store, nil, nil)
	service.now = func() time.Time {
		return time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	}

	result, err := service.DispatchPending(context.Background(), 1)

	require.Error(t, err)
	require.Contains(t, err.Error(), "restore unqueued microsoft alias task 42")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Failed)
}

func TestMicrosoftAliasProcessCreatesTwoAndWaitsForNextCalendarWeek(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	_, _, _, weekEnd := microsoftAliasQuotaWindows(now)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 1, Alias: "first123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
			{ID: 2, Alias: "second123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
		postCompleteUsage: &MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Aliases: []string{"first123456@outlook.com", "second123456@outlook.com"},
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	assert.Equal(t, 2, creator.count)
	assert.False(t, creator.reconcileOnly)
	require.Len(t, store.outcomes, 2)
	assert.Equal(t, MicrosoftAliasAttemptSucceeded, store.outcomes[0].Status)
	assert.Equal(t, MicrosoftAliasAttemptSucceeded, store.outcomes[1].Status)
	assert.Equal(t, now, store.completedAt)
	assert.Equal(t, weekEnd, store.deferredAt)
	assert.Empty(t, store.deferredSafe)
}

func TestMicrosoftAliasProcessHonorsCalendarWeeklyLimit(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	_, _, _, weekEnd := microsoftAliasQuotaWindows(now)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			ResourceStatus: "normal",
		},
		usage: MicrosoftAliasUsage{
			YearCount: 4,
			WeekCount: 2,
		},
		reserveUsage: MicrosoftAliasUsage{YearCount: 4, WeekCount: 2},
	}
	creator := &fakeMicrosoftAliasCreator{}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	assert.Zero(t, creator.count)
	assert.Equal(t, weekEnd, store.deferredAt)
}

func TestMicrosoftAliasProcessWaitsForNextYearAfterTenthAlias(t *testing.T) {
	now := time.Date(2026, time.December, 20, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 10, Alias: "tenth123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 10, WeekCount: 1},
		postCompleteUsage: &MicrosoftAliasUsage{YearCount: 10, WeekCount: 1},
	}
	creator := &fakeMicrosoftAliasCreator{
		candidates: []string{"tenth123456@outlook.com"},
		result: MicrosoftAliasCreationResult{
			Aliases: []string{"tenth123456@outlook.com"},
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	assert.Equal(t, 1, creator.count)
	require.Len(t, store.outcomes, 1)
	assert.Equal(t, MicrosoftAliasAttemptSucceeded, store.outcomes[0].Status)
	assert.Equal(t, time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC), store.deferredAt)
}

func TestMicrosoftAliasProcessDefersRateLimitUntilNextCalendarWeek(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	_, _, _, weekEnd := microsoftAliasQuotaWindows(now)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 1, Alias: "first123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
			{ID: 2, Alias: "second123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
		postCompleteUsage: &MicrosoftAliasUsage{},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Category:    "rate_limited",
			SafeMessage: "Microsoft alias creation is rate limited.",
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	require.Len(t, store.outcomes, 2)
	assert.Equal(t, MicrosoftAliasAttemptFailed, store.outcomes[0].Status)
	assert.Equal(t, MicrosoftAliasAttemptFailed, store.outcomes[1].Status)
	assert.Equal(t, weekEnd, store.deferredAt)
	assert.Equal(t, "Microsoft alias creation is rate limited.", store.deferredSafe)
}

func TestMicrosoftAliasProcessKeepsUncertainCandidateForReconciliation(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 1, Alias: "david123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
			{ID: 2, Alias: "liming654321@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
		postCompleteUsage: &MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Aliases:     []string{"david123456@outlook.com"},
			Attempted:   []string{"david123456@outlook.com", "liming654321@outlook.com"},
			Category:    "request",
			SafeMessage: "Microsoft alias result requires reconciliation.",
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	require.Len(t, store.outcomes, 2)
	assert.Equal(t, MicrosoftAliasAttemptSucceeded, store.outcomes[0].Status)
	assert.Equal(t, MicrosoftAliasAttemptUncertain, store.outcomes[1].Status)
	assert.Equal(t, now.Add(microsoftAliasTransientDelay(42, 1)), store.deferredAt)
	assert.True(t, store.deferredFailed)
}

func TestMicrosoftAliasProcessKeepsVerifyCodeFailureForReconciliation(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 1, Alias: "david123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
		postCompleteUsage: &MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Attempted:   []string{"david123456@outlook.com"},
			Category:    "code_error",
			SafeMessage: "Microsoft recovery mailbox verification failed.",
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	require.Len(t, store.outcomes, 1)
	assert.Equal(t, MicrosoftAliasAttemptUncertain, store.outcomes[0].Status)
	assert.Equal(t, now.Add(microsoftAliasTransientDelay(42, 1)), store.deferredAt)
	assert.True(t, store.deferredFailed)
}

func TestMicrosoftAliasProcessReleasesUnattemptedCandidatesAfterLoginFailure(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 1, Alias: "david123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
			{ID: 2, Alias: "liming654321@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
		postCompleteUsage: &MicrosoftAliasUsage{},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Category:    "request",
			SafeMessage: "Microsoft alias login failed temporarily.",
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	require.Len(t, store.outcomes, 2)
	assert.Equal(t, MicrosoftAliasAttemptFailed, store.outcomes[0].Status)
	assert.Equal(t, MicrosoftAliasAttemptFailed, store.outcomes[1].Status)
	assert.Equal(t, now.Add(microsoftAliasTransientDelay(42, 1)), store.deferredAt)
	assert.True(t, store.deferredFailed)
}

func TestMicrosoftAliasProcessPreservesPriorUncertainCandidateWhenReconciliationCannotStart(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{{
			ID:           1,
			Alias:        "david123456@outlook.com",
			Status:       MicrosoftAliasAttemptRunning,
			WasUncertain: true,
		}},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
		postCompleteUsage: &MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Category:    "request",
			SafeMessage: "Microsoft alias reconciliation could not start.",
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	require.Len(t, store.outcomes, 1)
	assert.Equal(t, MicrosoftAliasAttemptUncertain, store.outcomes[0].Status)
	assert.Equal(t, now.Add(microsoftAliasTransientDelay(42, 1)), store.deferredAt)
	assert.True(t, store.deferredFailed)
}

func TestMicrosoftAliasProcessHonorsExplicitUncertainCandidateAcrossProxyAttempts(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{{
			ID:     1,
			Alias:  "david123456@outlook.com",
			Status: MicrosoftAliasAttemptRunning,
		}},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
		postCompleteUsage: &MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Attempted: []string{"david123456@outlook.com"},
			Uncertain: []string{"david123456@outlook.com"},
			Category:  "mfa",
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	require.Len(t, store.outcomes, 1)
	assert.Equal(t, MicrosoftAliasAttemptUncertain, store.outcomes[0].Status)
	assert.True(t, store.paused)
}

func TestMicrosoftAliasProcessUsesCompletionTimeAcrossWeekBoundary(t *testing.T) {
	startedAt := time.Date(2026, time.July, 12, 15, 59, 0, 0, time.UTC)
	completedAt := time.Date(2026, time.July, 12, 16, 1, 0, 0, time.UTC)
	_, _, _, nextWeekEnd := microsoftAliasQuotaWindows(completedAt)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 1, Alias: "david123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
			{ID: 2, Alias: "liming654321@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage:      MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
		postCompleteUsage: &MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
	}
	creator := &fakeMicrosoftAliasCreator{
		result: MicrosoftAliasCreationResult{
			Aliases: []string{"david123456@outlook.com", "liming654321@outlook.com"},
		},
	}
	service := NewMicrosoftAliasService(store, nil, creator)
	call := 0
	service.now = func() time.Time {
		call++
		if call == 1 {
			return startedAt
		}
		return completedAt
	}

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	assert.Equal(t, completedAt, store.completedAt)
	assert.Equal(t, nextWeekEnd, store.deferredAt)
}

func TestMicrosoftAliasProcessStopsWhenResourceBecomesAbnormalBeforeRemoteCall(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	eligible := false
	store := &fakeMicrosoftAliasStore{
		claimed:  true,
		eligible: &eligible,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{
			{ID: 1, Alias: "david123456@outlook.com", Status: MicrosoftAliasAttemptRunning},
			{ID: 2, Alias: "liming654321@outlook.com", Status: MicrosoftAliasAttemptRunning},
		},
		reserveUsage: MicrosoftAliasUsage{YearCount: 2, WeekCount: 2},
	}
	creator := &fakeMicrosoftAliasCreator{}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	assert.Zero(t, creator.count)
	assert.Equal(t, 1, store.eligibilityChecks)
	assert.True(t, store.paused)
	require.Len(t, store.outcomes, 2)
	assert.Equal(t, MicrosoftAliasAttemptFailed, store.outcomes[0].Status)
	assert.Equal(t, MicrosoftAliasAttemptFailed, store.outcomes[1].Status)
}

func TestMicrosoftAliasProcessRequiresGraceAndThreeNegativeConfirmationsToReleaseUncertainAttempt(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	uncertainSince := now.Add(-microsoftAliasReconciliationGrace - time.Hour)
	lastNegative := now.Add(-2 * microsoftAliasNegativeConfirmationInterval)

	for _, test := range []struct {
		name                  string
		negativeConfirmations int
		expectedStatus        string
	}{
		{name: "second confirmation remains uncertain", negativeConfirmations: 1, expectedStatus: MicrosoftAliasAttemptUncertain},
		{name: "third confirmation releases quota", negativeConfirmations: 2, expectedStatus: MicrosoftAliasAttemptFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeMicrosoftAliasStore{
				claimed: true,
				account: &MicrosoftAliasAccount{
					ResourceID:     42,
					EmailAddress:   "owner@example.com",
					Password:       "secret",
					ResourceStatus: "normal",
				},
				reserveAttempts: []MicrosoftAliasAttempt{{
					ID:                         1,
					Alias:                      "david123456@outlook.com",
					Status:                     MicrosoftAliasAttemptRunning,
					WasUncertain:               true,
					WasAttempted:               true,
					UncertainSince:             &uncertainSince,
					NegativeConfirmations:      test.negativeConfirmations,
					LastNegativeConfirmationAt: &lastNegative,
				}},
				reserveUsage:      MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
				postCompleteUsage: &MicrosoftAliasUsage{},
			}
			creator := &fakeMicrosoftAliasCreator{
				result: MicrosoftAliasCreationResult{
					Absent:      []string{"david123456@outlook.com"},
					Category:    "alias_failed",
					SafeMessage: "Microsoft alias candidate is absent.",
				},
			}
			service := NewMicrosoftAliasService(store, nil, creator)
			service.now = func() time.Time { return now }

			require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
			assert.True(t, creator.reconcileOnly)
			require.Len(t, store.outcomes, 1)
			assert.Equal(t, test.expectedStatus, store.outcomes[0].Status)
			assert.True(t, store.outcomes[0].ReconciledAbsent)
		})
	}
}

func TestMicrosoftAliasProcessPausesPermanentCredentialFailure(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		claimed: true,
		account: &MicrosoftAliasAccount{
			ResourceID:     42,
			EmailAddress:   "owner@example.com",
			Password:       "bad-secret",
			ResourceStatus: "normal",
		},
		reserveAttempts: []MicrosoftAliasAttempt{{ID: 1, Alias: "david123456@outlook.com", Status: MicrosoftAliasAttemptRunning}},
		reserveUsage:    MicrosoftAliasUsage{YearCount: 1, WeekCount: 1},
	}
	creator := &fakeMicrosoftAliasCreator{result: MicrosoftAliasCreationResult{
		Category:    "password",
		SafeMessage: "Microsoft account password is incorrect.",
	}}
	service := NewMicrosoftAliasService(store, nil, creator)
	service.now = func() time.Time { return now }

	require.NoError(t, service.Process(context.Background(), microsoftAliasTestTask(42)))
	assert.True(t, store.paused)
	require.Len(t, store.outcomes, 1)
	assert.Equal(t, MicrosoftAliasAttemptFailed, store.outcomes[0].Status)
	assert.False(t, store.outcomes[0].Attempted)
}

func TestMicrosoftAliasTransientDelayUsesExponentialBackoffWithJitter(t *testing.T) {
	first := microsoftAliasTransientDelay(42, 1)
	second := microsoftAliasTransientDelay(42, 2)
	assert.Greater(t, second, first)
	assert.GreaterOrEqual(t, first, 12*time.Minute)
	assert.LessOrEqual(t, first, 18*time.Minute)
}

func TestMicrosoftAliasQuotaWindowsUseShanghaiCalendar(t *testing.T) {
	now := time.Date(2027, time.January, 1, 1, 0, 0, 0, time.UTC)
	yearStart, yearEnd, weekStart, weekEnd := microsoftAliasQuotaWindows(now)

	assert.Equal(t, time.Date(2026, time.December, 31, 16, 0, 0, 0, time.UTC), yearStart)
	assert.Equal(t, time.Date(2027, time.December, 31, 16, 0, 0, 0, time.UTC), yearEnd)
	assert.Equal(t, time.Date(2026, time.December, 27, 16, 0, 0, 0, time.UTC), weekStart)
	assert.Equal(t, time.Date(2027, time.January, 3, 16, 0, 0, 0, time.UTC), weekEnd)
}
