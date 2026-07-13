package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
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
	ensuredResourceID uint
	ensureResourceOK  bool
	ensureResourceErr error
	dispatchTasks     []MicrosoftAliasTask
	markDispatchErr   error
	adminSchedule     *MicrosoftAliasAdminSchedule
	adminScheduleErr  error
	expediteResult    *MicrosoftAliasExpediteResult
	expediteErr       error
	adminCommand      MicrosoftAliasExpediteCommand
	adminCommandLog   *governancedomain.OperationLog
	adminCommandReuse bool
}

func (f *fakeMicrosoftAliasStore) EnsureSchedules(context.Context, time.Time) (int64, error) {
	f.ensureCalls++
	return f.ensureResult, nil
}

func (f *fakeMicrosoftAliasStore) EnsureScheduleForResource(_ context.Context, resourceID uint, _ time.Time) (bool, error) {
	f.ensuredResourceID = resourceID
	return f.ensureResourceOK, f.ensureResourceErr
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

func (f *fakeMicrosoftAliasStore) BackfillExistingAliases(_ context.Context, _ uint, _ uint, _ []string) error {
	return nil
}

func (f *fakeMicrosoftAliasStore) GetAdminSchedule(context.Context, uint, time.Time, time.Time, time.Time, time.Time) (*MicrosoftAliasAdminSchedule, error) {
	if f.adminScheduleErr != nil {
		return nil, f.adminScheduleErr
	}
	if f.adminSchedule == nil {
		return &MicrosoftAliasAdminSchedule{}, nil
	}
	clone := *f.adminSchedule
	return &clone, nil
}

func (f *fakeMicrosoftAliasStore) AcceptAdminAliasExpedite(
	_ context.Context,
	command MicrosoftAliasExpediteCommand,
	_ time.Time,
	operationLog *governancedomain.OperationLog,
) (*MicrosoftAliasExpediteResult, bool, error) {
	f.adminCommand = command
	if operationLog != nil {
		clone := *operationLog
		f.adminCommandLog = &clone
	}
	if f.expediteErr != nil {
		return nil, false, f.expediteErr
	}
	if f.expediteResult == nil {
		return &MicrosoftAliasExpediteResult{}, f.adminCommandReuse, nil
	}
	clone := *f.expediteResult
	return &clone, f.adminCommandReuse, nil
}

type fakeMicrosoftAliasAdminQueue struct {
	dispatches int
	err        error
}

func (q *fakeMicrosoftAliasAdminQueue) EnqueueMicrosoftAlias(context.Context, MicrosoftAliasTask) error {
	return q.err
}

func (q *fakeMicrosoftAliasAdminQueue) EnqueueMicrosoftAliasDispatcher(context.Context, time.Duration) error {
	q.dispatches++
	return q.err
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

func TestMicrosoftAliasAdminScheduleReturnsSafeUsageAndLimits(t *testing.T) {
	nextRunAt := time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{adminSchedule: &MicrosoftAliasAdminSchedule{
		WeekCreated: 1,
		YearCreated: 4,
		NextRunAt:   &nextRunAt,
	}}
	service := NewMicrosoftAliasService(store, nil, nil)
	service.now = func() time.Time { return time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC) }

	schedule, err := service.GetAdminSchedule(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, 1, schedule.WeekCreated)
	assert.Equal(t, MicrosoftAliasWeeklyLimit, schedule.WeekLimit)
	assert.Equal(t, 4, schedule.YearCreated)
	assert.Equal(t, MicrosoftAliasYearlyLimit, schedule.YearLimit)
	assert.Equal(t, nextRunAt, *schedule.NextRunAt)
}

func TestMicrosoftAliasAdminCommandReturnsCanonicalTaskAndSafeAudit(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{expediteResult: &MicrosoftAliasExpediteResult{
		ResourceID:     42,
		Status:         "queued",
		QueuedAt:       now,
		UpdatedAt:      now,
		WakeDispatcher: true,
	}}
	queue := &fakeMicrosoftAliasAdminQueue{err: errors.New("redis unavailable")}
	service := NewMicrosoftAliasService(store, queue, nil)
	service.now = func() time.Time { return now }

	accepted, err := service.AcceptAdminExpedite(context.Background(), MicrosoftAliasExpediteCommand{
		ResourceID:     42,
		OperatorUserID: 7,
		IdempotencyKey: " alias-expedite-key ",
		RequestID:      "request-42",
		Path:           "/v1/admin/resources/:resourceId/aliases",
	})
	require.NoError(t, err)
	require.NotNil(t, accepted)
	assert.Equal(t, "alias_schedule:42", accepted.Task.TaskID())
	assert.Equal(t, governanceapp.AdminTaskBizMicrosoftResource, accepted.Task.BizType)
	assert.Equal(t, governanceapp.AdminTaskKindAlias, accepted.Task.Kind)
	assert.Equal(t, governanceapp.AdminTaskStatusQueued, accepted.Task.Status)
	assert.Equal(t, 1, accepted.Task.MaxAttempts)
	assert.Equal(t, "alias-expedite-key", store.adminCommand.IdempotencyKey)
	assert.Equal(t, uint(42), store.ensuredResourceID)
	require.NotNil(t, store.adminCommandLog)
	assert.Equal(t, uint(7), store.adminCommandLog.OperatorUserID)
	assert.Equal(t, "42", store.adminCommandLog.ResourceID)
	assert.NotContains(t, store.adminCommandLog.SafeSummary, "secret")
	assert.Equal(t, 1, queue.dispatches)
}

func TestMicrosoftAliasAdminCommandEnsuresMissingScheduleBeforeExpedite(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		ensureResourceOK: true,
		expediteResult: &MicrosoftAliasExpediteResult{
			ResourceID:     42,
			Status:         "queued",
			QueuedAt:       now,
			UpdatedAt:      now,
			WakeDispatcher: true,
		},
	}
	queue := &fakeMicrosoftAliasAdminQueue{}
	service := NewMicrosoftAliasService(store, queue, nil)
	service.now = func() time.Time { return now }

	accepted, err := service.AcceptAdminExpedite(context.Background(), MicrosoftAliasExpediteCommand{
		ResourceID:     42,
		OperatorUserID: 7,
		IdempotencyKey: "ensure-before-expedite",
	})
	require.NoError(t, err)
	require.NotNil(t, accepted)
	assert.Equal(t, uint(42), store.ensuredResourceID)
	assert.Equal(t, 1, queue.dispatches, "the accepted expedite wakes the dispatcher once")
}

func TestMicrosoftAliasAdminCommandReplaysReceiptAndValidatesIdempotency(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeMicrosoftAliasStore{
		adminCommandReuse: true,
		expediteResult: &MicrosoftAliasExpediteResult{
			ResourceID: 42,
			Status:     "running",
			QueuedAt:   now.Add(-time.Minute),
			UpdatedAt:  now,
		},
	}
	service := NewMicrosoftAliasService(store, nil, nil)
	service.now = func() time.Time { return now }

	accepted, err := service.AcceptAdminExpedite(context.Background(), MicrosoftAliasExpediteCommand{
		ResourceID:     42,
		OperatorUserID: 7,
		IdempotencyKey: "same-key",
	})
	require.NoError(t, err)
	assert.True(t, accepted.Reused)
	assert.Equal(t, governanceapp.AdminTaskStatusRunning, accepted.Task.Status)
	assert.Equal(t, 1, accepted.Task.Attempts)

	_, err = service.AcceptAdminExpedite(context.Background(), MicrosoftAliasExpediteCommand{
		ResourceID:     42,
		OperatorUserID: 7,
		IdempotencyKey: strings.Repeat("x", 129),
	})
	require.ErrorIs(t, err, ErrInvalidMicrosoftAliasExpedite)
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

func TestMicrosoftAliasValidationEnsuresOnlyTheValidatedSchedule(t *testing.T) {
	store := &fakeMicrosoftAliasStore{ensureResourceOK: true}
	queue := &fakeMicrosoftAliasAdminQueue{}
	service := NewMicrosoftAliasService(store, queue, nil)

	require.NoError(t, service.EnsureForValidatedMicrosoftResource(context.Background(), 42))
	assert.Equal(t, uint(42), store.ensuredResourceID)
	assert.Equal(t, 1, queue.dispatches)
	assert.Zero(t, store.ensureCalls, "validation must not start the broad daily scan")
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
