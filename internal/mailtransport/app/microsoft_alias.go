package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	MicrosoftAliasWeeklyLimit = 2
	MicrosoftAliasYearlyLimit = 10

	// The broad eligibility scan is deliberately daily. Immediate validation
	// success and an administrator expedite use the targeted path below, so the
	// periodic scanner is a recovery/backfill mechanism rather than a hot loop.
	microsoftAliasEnsureInterval = 24 * time.Hour

	microsoftAliasReconciliationGrace           = 24 * time.Hour
	microsoftAliasNegativeConfirmationInterval  = time.Hour
	microsoftAliasRequiredNegativeConfirmations = 3
	microsoftAliasTransientBackoffBase          = 15 * time.Minute
	microsoftAliasTransientBackoffMax           = 12 * time.Hour
	MicrosoftAliasAttemptRunning                = "running"
	MicrosoftAliasAttemptSucceeded              = "succeeded"
	MicrosoftAliasAttemptFailed                 = "failed"
	MicrosoftAliasAttemptUncertain              = "uncertain"

	MicrosoftAliasResourceNotNormalMessage = "Microsoft resource is not in normal state for alias creation."
	// MicrosoftAliasExternalRecoveryMessage marks accounts whose recovery mailbox
	// is on an external (non-binding) domain: we cannot receive OTP codes there,
	// so explicit-alias creation can never succeed and the schedule is skipped.
	MicrosoftAliasExternalRecoveryMessage = "Recovery mailbox is external; explicit-alias creation is skipped."
	// MicrosoftAliasBindingUnresolvedMessage marks accounts whose recovery
	// mailbox is absent or still masked after preflight.
	MicrosoftAliasBindingUnresolvedMessage = "Recovery mailbox address is unresolved; explicit-alias creation is paused."
)

var microsoftAliasQuotaLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var (
	ErrMicrosoftAliasStaleClaim       = errors.New("stale microsoft alias claim")
	ErrMicrosoftAliasOwnerUnavailable = errors.New("microsoft alias super administrator owner is unavailable")
)

type MicrosoftAliasAccount struct {
	ResourceID     uint
	EmailAddress   string
	Password       string
	BindingAddress string
	ResourceStatus string
	FailureStreak  int
	ClaimToken     string
}

type MicrosoftAliasUsage struct {
	YearCount int
	WeekCount int
}

type MicrosoftAliasAttempt struct {
	ID                         uint
	Alias                      string
	Status                     string
	WasUncertain               bool
	WasAttempted               bool
	UncertainSince             *time.Time
	NegativeConfirmations      int
	LastNegativeConfirmationAt *time.Time
}

type MicrosoftAliasAttemptOutcome struct {
	AttemptID        uint
	Status           string
	Category         string
	SafeMessage      string
	Attempted        bool
	ReconciledAbsent bool
}

type MicrosoftAliasCreationRequest struct {
	ResourceID     uint
	EmailAddress   string
	Password       string
	BindingAddress string
	RecoveryMask   string
	BindingMissing bool
	Candidates     []string
	ReconcileOnly  bool
}

type MicrosoftAliasCreationResult struct {
	Aliases         []string
	Attempted       []string
	Uncertain       []string
	Absent          []string
	ExistingAliases []string // all aliases found on the account at login time (for backfill reconciliation)
	Category        string
	SafeMessage     string
	ProxyFailure    bool
}

type MicrosoftAliasBindingPreparationResult struct {
	BindingAddress       string
	Category             string
	SafeMessage          string
	ProxyFailure         bool
	ReleaseRecoveryLease func(context.Context) error
}

type MicrosoftAliasScheduleStore interface {
	EnsureSchedules(ctx context.Context, now time.Time) (int64, error)
	EnsureScheduleForResource(ctx context.Context, resourceID uint, now time.Time) (bool, error)
	FindDispatchable(ctx context.Context, limit int, now, _ time.Time, _ time.Time) ([]MicrosoftAliasTask, error)
	MarkQueued(ctx context.Context, task MicrosoftAliasTask, now time.Time) (bool, error)
	Claim(ctx context.Context, task MicrosoftAliasTask, now time.Time) (*MicrosoftAliasAccount, bool, error)
	CheckEligibility(ctx context.Context, resourceID uint, claimToken string) (bool, string, error)
	ReloadEligibleAccount(ctx context.Context, resourceID uint, claimToken string) (*MicrosoftAliasAccount, bool, string, error)
	SaveBindingAddress(ctx context.Context, resourceID uint, claimToken, expectedAddress, bindingAddress string) error
	Reserve(ctx context.Context, resourceID uint, claimToken string, candidates []string, yearStart, yearEnd, weekStart, weekEnd, now time.Time) ([]MicrosoftAliasAttempt, MicrosoftAliasUsage, error)
	Usage(ctx context.Context, resourceID uint, yearStart, yearEnd, weekStart, weekEnd time.Time) (MicrosoftAliasUsage, error)
	Complete(ctx context.Context, resourceID uint, claimToken string, outcomes []MicrosoftAliasAttemptOutcome, completedAt time.Time) error
	Defer(ctx context.Context, resourceID uint, claimToken string, nextRunAt time.Time, safeError string, failed bool) error
	Pause(ctx context.Context, resourceID uint, claimToken string, safeError string) error
	MarkDispatchFailed(ctx context.Context, task MicrosoftAliasTask, nextRunAt time.Time, safeError string) error
	BackfillExistingAliases(ctx context.Context, resourceID uint, aliases []string) error
}

// EnsureScheduleForResource records or wakes an eligible resource's durable
// alias schedule. It never calls Microsoft directly; the normal dispatcher
// claims and executes the work later.
func (s *MicrosoftAliasService) EnsureScheduleForResource(ctx context.Context, resourceID uint) (bool, error) {
	if s == nil || s.store == nil || resourceID == 0 {
		return false, nil
	}
	ensured, err := s.store.EnsureScheduleForResource(ctx, resourceID, s.now().UTC())
	if err != nil {
		return false, err
	}
	return ensured, nil
}

// EnsureForValidatedMicrosoftResource records or wakes the durable alias
// schedule after a successful Microsoft validation. A schedule change wakes
// the existing dispatcher; no external request runs in this call.
func (s *MicrosoftAliasService) EnsureForValidatedMicrosoftResource(ctx context.Context, resourceID uint) error {
	ensured, err := s.EnsureScheduleForResource(ctx, resourceID)
	if err != nil {
		return err
	}
	if ensured {
		s.ScheduleDispatcher(ctx, 0)
	}
	return nil
}

type MicrosoftAliasQueue interface {
	EnqueueMicrosoftAlias(ctx context.Context, task MicrosoftAliasTask) (bool, error)
	EnqueueMicrosoftAliasDispatcher(ctx context.Context, delay time.Duration) error
}

type MicrosoftAliasCreator interface {
	PrepareMicrosoftAliasBinding(ctx context.Context, req MicrosoftAliasCreationRequest) (MicrosoftAliasBindingPreparationResult, error)
	GenerateMicrosoftAliasCandidates(count int) ([]string, error)
	CreateMicrosoftAliases(ctx context.Context, req MicrosoftAliasCreationRequest) (MicrosoftAliasCreationResult, error)
}

type MicrosoftAliasTask struct {
	ResourceID uint   `json:"resourceId"`
	Generation uint64 `json:"generation"`
}

type MicrosoftAliasDispatchResult struct {
	Ensured   int64
	Attempted int
	Queued    int
	Failed    int
}

type MicrosoftAliasService struct {
	store        MicrosoftAliasScheduleStore
	queue        MicrosoftAliasQueue
	creator      MicrosoftAliasCreator
	now          func() time.Time
	ensureMu     sync.Mutex
	lastEnsureAt time.Time
}

func (s *MicrosoftAliasService) BackfillExistingAliases(ctx context.Context, resourceID uint, aliases []string) error {
	if s == nil || s.store == nil {
		return errors.New("microsoft alias store is unavailable")
	}
	return s.store.BackfillExistingAliases(ctx, resourceID, aliases)
}

func NewMicrosoftAliasService(store MicrosoftAliasScheduleStore, queue MicrosoftAliasQueue, creator MicrosoftAliasCreator) *MicrosoftAliasService {
	return &MicrosoftAliasService{
		store:   store,
		queue:   queue,
		creator: creator,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (s *MicrosoftAliasService) DispatchPending(ctx context.Context, limit int) (*MicrosoftAliasDispatchResult, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("microsoft alias schedule store is unavailable")
	}
	if limit <= 0 {
		limit = 10
	}
	now := s.now().UTC()
	var ensured int64
	if s.beginEnsureSchedules(now) {
		var err error
		ensured, err = s.store.EnsureSchedules(ctx, now)
		if err != nil {
			s.resetScheduleEnsure()
			return nil, fmt.Errorf("ensure microsoft alias schedules: %w", err)
		}
	}
	tasks, err := s.store.FindDispatchable(ctx, limit, now, time.Time{}, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("find dispatchable microsoft alias schedules: %w", err)
	}
	result := &MicrosoftAliasDispatchResult{Ensured: ensured, Attempted: len(tasks)}
	var dispatchErr error
	for _, task := range tasks {
		if s.queue == nil {
			result.Failed++
			continue
		}
		accepted, err := s.queue.EnqueueMicrosoftAlias(ctx, task)
		if err != nil {
			result.Failed++
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf("enqueue microsoft alias task %d: %w", task.ResourceID, err))
			continue
		}
		if !accepted {
			continue
		}
		queued, err := s.store.MarkQueued(ctx, task, now)
		if err != nil {
			result.Failed++
			dispatchErr = errors.Join(dispatchErr, fmt.Errorf("activate microsoft alias task %d: %w", task.ResourceID, err))
			continue
		}
		if queued {
			result.Queued++
		}
	}
	return result, dispatchErr
}

func (s *MicrosoftAliasService) beginEnsureSchedules(now time.Time) bool {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if !s.lastEnsureAt.IsZero() && now.Sub(s.lastEnsureAt) < microsoftAliasEnsureInterval {
		return false
	}
	s.lastEnsureAt = now
	return true
}

// resetScheduleEnsure permits an earlier retry only after the daily scan
// itself failed before it could persist any scheduling decision.
func (s *MicrosoftAliasService) resetScheduleEnsure() {
	s.ensureMu.Lock()
	s.lastEnsureAt = time.Time{}
	s.ensureMu.Unlock()
}

func (s *MicrosoftAliasService) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if s == nil || s.queue == nil {
		return
	}
	_ = s.queue.EnqueueMicrosoftAliasDispatcher(ctx, delay)
}

func (s *MicrosoftAliasService) Process(ctx context.Context, task MicrosoftAliasTask) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("microsoft alias schedule store is unavailable")
	}
	if task.ResourceID == 0 || task.Generation == 0 {
		return fmt.Errorf("microsoft alias resource is missing")
	}

	now := s.now().UTC()
	if _, err := s.store.MarkQueued(ctx, task, now); err != nil {
		return fmt.Errorf("activate microsoft alias schedule: %w", err)
	}
	account, claimed, err := s.store.Claim(ctx, task, now)
	if err != nil {
		return fmt.Errorf("claim microsoft alias schedule: %w", err)
	}
	if !claimed || account == nil {
		return nil
	}
	yearStart, yearEnd, weekStart, weekEnd := microsoftAliasQuotaWindows(now)
	if account.ResourceStatus != "normal" {
		return ignoreStaleAliasClaim(s.store.Pause(ctx, task.ResourceID, account.ClaimToken, MicrosoftAliasResourceNotNormalMessage))
	}
	if s.creator == nil {
		next := now.Add(microsoftAliasTransientDelay(task.ResourceID, account.FailureStreak+1))
		return ignoreStaleAliasClaim(s.store.Defer(ctx, task.ResourceID, account.ClaimToken, next, "Microsoft alias service is temporarily unavailable.", true))
	}
	prepared, prepareErr := s.creator.PrepareMicrosoftAliasBinding(ctx, MicrosoftAliasCreationRequest{
		ResourceID:     account.ResourceID,
		EmailAddress:   account.EmailAddress,
		Password:       account.Password,
		BindingAddress: account.BindingAddress,
	})
	if prepared.ReleaseRecoveryLease != nil {
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = prepared.ReleaseRecoveryLease(releaseCtx)
			cancel()
		}()
	}
	recoveryMask := ""
	bindingMissing := strings.TrimSpace(account.BindingAddress) == ""
	if strings.Contains(strings.SplitN(strings.ToLower(strings.TrimSpace(account.BindingAddress)), "@", 2)[0], "*") {
		recoveryMask = strings.ToLower(strings.TrimSpace(account.BindingAddress))
	}
	prepared.BindingAddress = strings.ToLower(strings.TrimSpace(prepared.BindingAddress))
	if prepareErr != nil || prepared.ProxyFailure {
		if prepared.BindingAddress != "" && prepared.BindingAddress != strings.ToLower(strings.TrimSpace(account.BindingAddress)) {
			if err := s.store.SaveBindingAddress(ctx, task.ResourceID, account.ClaimToken, account.BindingAddress, prepared.BindingAddress); errors.Is(err, ErrMicrosoftAliasStaleClaim) {
				return nil
			} else if err != nil {
				return fmt.Errorf("save observed microsoft alias binding address: %w", err)
			}
		}
		next := now.Add(microsoftAliasTransientDelay(task.ResourceID, account.FailureStreak+1))
		return ignoreStaleAliasClaim(s.store.Defer(ctx, task.ResourceID, account.ClaimToken, next, "Microsoft alias service is temporarily unavailable.", true))
	}
	if prepared.Category == "external_binding" {
		if prepared.BindingAddress != "" && prepared.BindingAddress != strings.ToLower(strings.TrimSpace(account.BindingAddress)) {
			if err := s.store.SaveBindingAddress(ctx, task.ResourceID, account.ClaimToken, account.BindingAddress, prepared.BindingAddress); errors.Is(err, ErrMicrosoftAliasStaleClaim) {
				return nil
			} else if err != nil {
				return fmt.Errorf("save external microsoft alias binding address: %w", err)
			}
		}
		return ignoreStaleAliasClaim(s.store.Pause(ctx, task.ResourceID, account.ClaimToken, MicrosoftAliasExternalRecoveryMessage))
	}
	if isPermanentAliasCategory(prepared.Category) {
		message := safeAliasMessage(prepared.SafeMessage)
		if message == "" {
			message = defaultAliasSafeMessage(prepared.Category)
		}
		return ignoreStaleAliasClaim(s.store.Pause(ctx, task.ResourceID, account.ClaimToken, message))
	}
	if prepared.BindingAddress == "" {
		next := now.Add(microsoftAliasTransientDelay(task.ResourceID, account.FailureStreak+1))
		return ignoreStaleAliasClaim(s.store.Defer(ctx, task.ResourceID, account.ClaimToken, next, MicrosoftAliasBindingUnresolvedMessage, true))
	}
	if err := s.store.SaveBindingAddress(ctx, task.ResourceID, account.ClaimToken, account.BindingAddress, prepared.BindingAddress); errors.Is(err, ErrMicrosoftAliasStaleClaim) {
		return nil
	} else if err != nil {
		return fmt.Errorf("save microsoft alias binding address: %w", err)
	}
	if prepared.ReleaseRecoveryLease != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := prepared.ReleaseRecoveryLease(releaseCtx)
		cancel()
		if err != nil {
			next := now.Add(microsoftAliasTransientDelay(task.ResourceID, account.FailureStreak+1))
			return ignoreStaleAliasClaim(s.store.Defer(ctx, task.ResourceID, account.ClaimToken, next, "Microsoft alias service is temporarily unavailable.", true))
		}
	}
	currentAccount, eligible, ineligibleMessage, err := s.store.ReloadEligibleAccount(ctx, task.ResourceID, account.ClaimToken)
	if errors.Is(err, ErrMicrosoftAliasStaleClaim) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recheck microsoft alias eligibility before reservation: %w", err)
	}
	if !eligible || currentAccount == nil {
		return s.pauseIneligibleAttempts(ctx, task.ResourceID, account.ClaimToken, nil, ineligibleMessage, s.now().UTC())
	}
	account = currentAccount

	candidates, err := s.creator.GenerateMicrosoftAliasCandidates(MicrosoftAliasWeeklyLimit)
	if err != nil {
		next := now.Add(microsoftAliasTransientDelay(task.ResourceID, account.FailureStreak+1))
		return ignoreStaleAliasClaim(s.store.Defer(ctx, task.ResourceID, account.ClaimToken, next, "Microsoft alias service is temporarily unavailable.", true))
	}
	attempts, usage, err := s.store.Reserve(
		ctx,
		task.ResourceID,
		account.ClaimToken,
		candidates,
		yearStart,
		yearEnd,
		weekStart,
		weekEnd,
		now,
	)
	if errors.Is(err, ErrMicrosoftAliasStaleClaim) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reserve microsoft alias quota: %w", err)
	}
	if len(attempts) == 0 {
		next := now.Add(time.Minute)
		if usage.YearCount >= MicrosoftAliasYearlyLimit {
			next = yearEnd
		} else if usage.WeekCount >= MicrosoftAliasWeeklyLimit {
			next = weekEnd
		}
		return ignoreStaleAliasClaim(s.store.Defer(ctx, task.ResourceID, account.ClaimToken, next, "", false))
	}

	reservedAliases := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		reservedAliases = append(reservedAliases, attempt.Alias)
	}

	currentAccount, eligible, ineligibleMessage, err = s.store.ReloadEligibleAccount(ctx, task.ResourceID, account.ClaimToken)
	if errors.Is(err, ErrMicrosoftAliasStaleClaim) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recheck microsoft alias eligibility before remote request: %w", err)
	}
	if !eligible || currentAccount == nil {
		return s.pauseIneligibleAttempts(ctx, task.ResourceID, account.ClaimToken, attempts, ineligibleMessage, s.now().UTC())
	}
	account = currentAccount

	result, createErr := s.creator.CreateMicrosoftAliases(ctx, MicrosoftAliasCreationRequest{
		ResourceID:     account.ResourceID,
		EmailAddress:   account.EmailAddress,
		Password:       account.Password,
		BindingAddress: account.BindingAddress,
		RecoveryMask:   recoveryMask,
		BindingMissing: bindingMissing,
		Candidates:     reservedAliases,
	})
	if createErr != nil {
		result = MicrosoftAliasCreationResult{
			Category:    "request",
			SafeMessage: "Microsoft alias service is temporarily unavailable.",
		}
	}
	return s.completeAliasResult(ctx, task, account, attempts, result)
}

func (s *MicrosoftAliasService) completeAliasResult(
	ctx context.Context,
	task MicrosoftAliasTask,
	account *MicrosoftAliasAccount,
	attempts []MicrosoftAliasAttempt,
	result MicrosoftAliasCreationResult,
) error {

	confirmed := make(map[string]struct{}, len(result.Aliases))
	for _, alias := range normalizeExplicitAliases(result.Aliases) {
		confirmed[alias] = struct{}{}
	}
	attempted := make(map[string]struct{}, len(result.Attempted))
	for _, alias := range normalizeExplicitAliases(result.Attempted) {
		attempted[alias] = struct{}{}
	}
	uncertain := make(map[string]struct{}, len(result.Uncertain))
	for _, alias := range normalizeExplicitAliases(result.Uncertain) {
		uncertain[alias] = struct{}{}
	}
	absent := make(map[string]struct{}, len(result.Absent))
	for _, alias := range normalizeExplicitAliases(result.Absent) {
		absent[alias] = struct{}{}
	}
	resultCategory := strings.TrimSpace(result.Category)
	message := safeAliasMessage(result.SafeMessage)
	if message == "" && (resultCategory != "" || len(confirmed) != len(attempts)) {
		message = defaultAliasSafeMessage(resultCategory)
	}
	completedAt := s.now().UTC()
	outcomes := make([]MicrosoftAliasAttemptOutcome, 0, len(attempts))
	hasUncertain := false
	for _, attempt := range attempts {
		key := strings.ToLower(attempt.Alias)
		status := MicrosoftAliasAttemptFailed
		category := resultCategory
		safeMessage := message
		_, wasAttempted := attempted[key]
		_, explicitlyUncertain := uncertain[key]
		_, reconciledAbsent := absent[key]
		if _, ok := confirmed[key]; ok {
			status = MicrosoftAliasAttemptSucceeded
			category = "added"
			safeMessage = ""
		} else if explicitlyUncertain || (wasAttempted && isRetryableAliasCategory(category)) {
			status = MicrosoftAliasAttemptUncertain
			hasUncertain = true
		} else if attempt.WasUncertain && !wasAttempted {
			if !reconciledAbsent || !microsoftAliasReconciliationCanRelease(attempt, completedAt) {
				status = MicrosoftAliasAttemptUncertain
				hasUncertain = true
			}
		}
		outcomes = append(outcomes, MicrosoftAliasAttemptOutcome{
			AttemptID:        attempt.ID,
			Status:           status,
			Category:         category,
			SafeMessage:      safeMessage,
			Attempted:        wasAttempted,
			ReconciledAbsent: reconciledAbsent && !wasAttempted,
		})
	}
	if err := s.store.Complete(ctx, task.ResourceID, account.ClaimToken, outcomes, completedAt); errors.Is(err, ErrMicrosoftAliasStaleClaim) {
		return nil
	} else if err != nil {
		return fmt.Errorf("complete microsoft alias attempts: %w", err)
	}

	// Backfill externally-listed aliases into the local DB. The store owns the
	// fixed platform-owner invariant; resource IDs and resource owners must
	// never be accepted as explicit-alias owners here.
	if len(result.ExistingAliases) > 0 {
		if err := s.store.BackfillExistingAliases(ctx, task.ResourceID, result.ExistingAliases); err != nil {
			return fmt.Errorf("backfill existing microsoft aliases: %w", err)
		}
	}

	yearStart, yearEnd, weekStart, weekEnd := microsoftAliasQuotaWindows(completedAt)
	usage, err := s.store.Usage(ctx, task.ResourceID, yearStart, yearEnd, weekStart, weekEnd)
	if err != nil {
		return fmt.Errorf("reload microsoft alias usage: %w", err)
	}
	if isPermanentAliasCategory(resultCategory) {
		return ignoreStaleAliasClaim(s.store.Pause(ctx, task.ResourceID, account.ClaimToken, message))
	}

	next := completedAt.Add(time.Minute)
	failed := false
	if hasUncertain {
		next = completedAt.Add(microsoftAliasTransientDelay(task.ResourceID, account.FailureStreak+1))
		failed = true
	} else if usage.YearCount >= MicrosoftAliasYearlyLimit {
		next = yearEnd
	} else if usage.WeekCount >= MicrosoftAliasWeeklyLimit {
		next = weekEnd
	} else {
		switch resultCategory {
		case "rate_limited":
			next = weekEnd
			failed = true
		case "request", "auth_timeout", "code_timeout", "code_error", "alias_failed", "alias_exists":
			next = completedAt.Add(microsoftAliasTransientDelay(task.ResourceID, account.FailureStreak+1))
			failed = true
		}
	}
	return ignoreStaleAliasClaim(s.store.Defer(ctx, task.ResourceID, account.ClaimToken, next, message, failed))
}

func (s *MicrosoftAliasService) pauseIneligibleAttempts(
	ctx context.Context,
	resourceID uint,
	claimToken string,
	attempts []MicrosoftAliasAttempt,
	safeMessage string,
	completedAt time.Time,
) error {
	safeMessage = strings.TrimSpace(safeMessage)
	if safeMessage == "" {
		safeMessage = MicrosoftAliasResourceNotNormalMessage
	}
	outcomes := make([]MicrosoftAliasAttemptOutcome, 0, len(attempts))
	for _, attempt := range attempts {
		status := MicrosoftAliasAttemptFailed
		if attempt.WasUncertain || attempt.WasAttempted {
			status = MicrosoftAliasAttemptUncertain
		}
		outcomes = append(outcomes, MicrosoftAliasAttemptOutcome{
			AttemptID:   attempt.ID,
			Status:      status,
			Category:    "not_eligible",
			SafeMessage: safeMessage,
		})
	}
	if len(outcomes) > 0 {
		if err := s.store.Complete(ctx, resourceID, claimToken, outcomes, completedAt); errors.Is(err, ErrMicrosoftAliasStaleClaim) {
			return nil
		} else if err != nil {
			return fmt.Errorf("complete ineligible microsoft alias attempts: %w", err)
		}
	}
	return ignoreStaleAliasClaim(s.store.Pause(ctx, resourceID, claimToken, safeMessage))
}

func microsoftAliasReconciliationCanRelease(attempt MicrosoftAliasAttempt, now time.Time) bool {
	if !attempt.WasAttempted || attempt.UncertainSince == nil || now.Before(*attempt.UncertainSince) {
		return false
	}
	if now.Sub(*attempt.UncertainSince) < microsoftAliasReconciliationGrace {
		return false
	}
	confirmations := attempt.NegativeConfirmations
	if attempt.LastNegativeConfirmationAt == nil || now.Sub(*attempt.LastNegativeConfirmationAt) >= microsoftAliasNegativeConfirmationInterval {
		confirmations++
	}
	return confirmations >= microsoftAliasRequiredNegativeConfirmations
}

func microsoftAliasTransientDelay(resourceID uint, failureStreak int) time.Duration {
	if failureStreak < 1 {
		failureStreak = 1
	}
	shift := failureStreak - 1
	if shift > 6 {
		shift = 6
	}
	delay := microsoftAliasTransientBackoffBase * time.Duration(1<<shift)
	seed := uint64(resourceID)*0x9e3779b97f4a7c15 + uint64(failureStreak)*0xbf58476d1ce4e5b9
	jitterPercent := 80 + seed%41
	jittered := time.Duration(int64(delay) * int64(jitterPercent) / 100)
	if jittered > microsoftAliasTransientBackoffMax {
		return microsoftAliasTransientBackoffMax
	}
	return jittered
}

func isPermanentAliasCategory(category string) bool {
	switch strings.TrimSpace(category) {
	case "password", "unknown_mailbox", "mfa", "passkey", "phone", "locked", "account_abnormal", "already_bound":
		return true
	default:
		return false
	}
}

func microsoftAliasQuotaWindows(now time.Time) (yearStart, yearEnd, weekStart, weekEnd time.Time) {
	local := now.In(microsoftAliasQuotaLocation)
	yearStartLocal := time.Date(local.Year(), time.January, 1, 0, 0, 0, 0, microsoftAliasQuotaLocation)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	weekStartLocal := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, microsoftAliasQuotaLocation).
		AddDate(0, 0, -daysSinceMonday)
	return yearStartLocal.UTC(), yearStartLocal.AddDate(1, 0, 0).UTC(), weekStartLocal.UTC(), weekStartLocal.AddDate(0, 0, 7).UTC()
}

func isRetryableAliasCategory(category string) bool {
	switch strings.TrimSpace(category) {
	case "request", "auth_timeout", "code_timeout", "code_error":
		return true
	default:
		return false
	}
}

func ignoreStaleAliasClaim(err error) error {
	if errors.Is(err, ErrMicrosoftAliasStaleClaim) {
		return nil
	}
	return err
}

func defaultAliasSafeMessage(category string) string {
	switch strings.TrimSpace(category) {
	case "rate_limited":
		return "Microsoft alias creation is rate limited."
	case "password":
		return "Microsoft account password is incorrect."
	case "mfa", "passkey", "phone":
		return "Microsoft account requires additional verification."
	case "request", "auth_timeout":
		return "Microsoft alias service is temporarily unavailable."
	default:
		return "Microsoft alias creation failed."
	}
}

func normalizeExplicitAliases(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !strings.HasSuffix(value, "@outlook.com") || strings.Count(value, "@") != 1 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func safeAliasMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
