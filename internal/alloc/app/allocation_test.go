package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
)

type candidateRefreshRepoStub struct {
	Repository
	pending          []domain.CandidateRefresh
	events           []string
	markProcessing   bool
	runCurrent       bool
	runErr           error
	releaseCalls     int
	recordCalls      int
	recordedAbnormal bool
}

func (r *candidateRefreshRepoStub) ListPendingCandidateRefreshes(context.Context, int) ([]domain.CandidateRefresh, error) {
	return append([]domain.CandidateRefresh(nil), r.pending...), nil
}

func (r *candidateRefreshRepoStub) MarkCandidateRefreshProcessing(context.Context, uint, uint64) (bool, error) {
	r.events = append(r.events, "processing")
	return r.markProcessing, nil
}

func (r *candidateRefreshRepoStub) RunCandidateRefresh(context.Context, uint, uint64) (int, bool, error) {
	return 0, r.runCurrent, r.runErr
}

func (r *candidateRefreshRepoStub) ReleaseCandidateRefreshInfrastructureFailure(context.Context, uint, uint64, string) (bool, error) {
	r.releaseCalls++
	return true, nil
}

func (r *candidateRefreshRepoStub) RecordCandidateRefreshFailure(context.Context, uint, uint64, string) (bool, bool, error) {
	r.recordCalls++
	r.recordedAbnormal = r.recordCalls >= 3
	return true, r.recordedAbnormal, nil
}

type candidateRefreshQueueStub struct {
	accepted        bool
	err             error
	events          *[]string
	dispatcherCalls int
}

func (q *candidateRefreshQueueStub) EnqueueCandidateRefresh(context.Context, CandidateRefreshTask) (bool, error) {
	if q.events != nil {
		*q.events = append(*q.events, "enqueue")
	}
	return q.accepted, q.err
}

func (q *candidateRefreshQueueStub) EnqueueCandidateRefreshDispatcher(context.Context, time.Duration) error {
	q.dispatcherCalls++
	return nil
}

func (q *candidateRefreshQueueStub) EnqueueInventoryRefresh(context.Context) error {
	return nil
}

func TestCandidateRefreshMarksProcessingOnlyAfterAcceptedEnqueue(t *testing.T) {
	tests := []struct {
		name             string
		accepted         bool
		queueErr         error
		wantEvents       []string
		wantQueued       int
		wantDispatchFail int
	}{
		{name: "accepted", accepted: true, wantEvents: []string{"enqueue", "processing"}, wantQueued: 1},
		{name: "duplicate", wantEvents: []string{"enqueue"}},
		{name: "redis failure", queueErr: errors.New("redis unavailable"), wantEvents: []string{"enqueue"}, wantDispatchFail: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &candidateRefreshRepoStub{
				pending:        []domain.CandidateRefresh{{ProjectID: 10, Generation: 7, Status: domain.CandidateRefreshPending}},
				markProcessing: true,
			}
			queue := &candidateRefreshQueueStub{accepted: tt.accepted, err: tt.queueErr, events: &repo.events}
			result, err := NewUseCase(repo, queue).DispatchCandidateRefreshes(context.Background(), 100)
			if err != nil {
				t.Fatalf("DispatchCandidateRefreshes() error = %v", err)
			}
			if !slices.Equal(repo.events, tt.wantEvents) {
				t.Fatalf("events = %v, want %v", repo.events, tt.wantEvents)
			}
			if result.Queued != tt.wantQueued || result.Failed != tt.wantDispatchFail {
				t.Fatalf("result = %#v, want queued=%d failed=%d", result, tt.wantQueued, tt.wantDispatchFail)
			}
		})
	}
}

func TestCandidateRefreshInfrastructureFailureDoesNotCountBusinessFailure(t *testing.T) {
	repo := &candidateRefreshRepoStub{
		runCurrent: true,
		runErr:     fmt.Errorf("%w: database disconnected", domain.ErrCandidateRefreshInfrastructure),
	}
	err := NewUseCase(repo).ProcessCandidateRefresh(context.Background(), CandidateRefreshTask{ProjectID: 10, Generation: 7})
	if !errors.Is(err, domain.ErrCandidateRefreshInfrastructure) {
		t.Fatalf("ProcessCandidateRefresh() error = %v", err)
	}
	if repo.releaseCalls != 1 || repo.recordCalls != 0 {
		t.Fatalf("release calls = %d, record calls = %d; want 1, 0", repo.releaseCalls, repo.recordCalls)
	}
}

func TestCandidateRefreshThirdBusinessFailureBecomesAbnormal(t *testing.T) {
	businessErr := errors.New("candidate refresh rejected")
	repo := &candidateRefreshRepoStub{runCurrent: true, runErr: businessErr}
	queue := &candidateRefreshQueueStub{}
	uc := NewUseCase(repo, queue)
	for attempt := 1; attempt <= 3; attempt++ {
		err := uc.ProcessCandidateRefresh(context.Background(), CandidateRefreshTask{ProjectID: 10, Generation: uint64(attempt)})
		if !errors.Is(err, businessErr) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
	if repo.recordCalls != 3 || !repo.recordedAbnormal {
		t.Fatalf("record calls = %d, abnormal = %v; want 3, true", repo.recordCalls, repo.recordedAbnormal)
	}
	if queue.dispatcherCalls != 2 {
		t.Fatalf("dispatcher calls = %d, want 2 before terminal failure", queue.dispatcherCalls)
	}
}

type generatedMailboxRetryRepo struct {
	Repository
	candidate DomainCandidate
	calls     int
}

func (*generatedMailboxRetryRepo) LockResourceRoot(context.Context, uint, domain.AllocationType) (bool, error) {
	return true, nil
}

func (r *generatedMailboxRetryRepo) LockDomainCandidate(context.Context, uint, uint, domain.SupplyScope, string) (*DomainCandidate, error) {
	return &r.candidate, nil
}

func (*generatedMailboxRetryRepo) EnsureDailyUsageAvailable(context.Context, string, domain.AllocationType, uint, domain.DailyUsageKind, int) error {
	return nil
}

func (*generatedMailboxRetryRepo) FindReusableGeneratedMailbox(context.Context, uint, uint) (*GeneratedMailboxCandidate, error) {
	return nil, nil
}

func (r *generatedMailboxRetryRepo) FindOrCreateGeneratedMailbox(_ context.Context, _ uint, _ uint, email string) (*GeneratedMailboxCandidate, error) {
	r.calls++
	if r.calls == 1 {
		return nil, domain.ErrAllocationConflict
	}
	return &GeneratedMailboxCandidate{ID: 7, Email: email}, nil
}

func (*generatedMailboxRetryRepo) CreateDomainAllocation(_ context.Context, allocation *domain.GeneratedMailboxAllocation) error {
	allocation.ID = 8
	allocation.CreatedAt = time.Now().UTC()
	return nil
}

func (*generatedMailboxRetryRepo) ConsumeDailyUsage(context.Context, string, domain.AllocationType, uint, domain.DailyUsageKind, int) error {
	return nil
}

func (*generatedMailboxRetryRepo) TouchDomainAllocated(context.Context, uint, uint, time.Time) error {
	return nil
}

func TestGeneratedMailboxVariantsUseHumanNamesAndUpToSixDigits(t *testing.T) {
	if len(biblicalMailboxNames) < 1_000 {
		t.Fatalf("got %d biblical names, want at least 1000", len(biblicalMailboxNames))
	}
	names := make(map[string]struct{}, generatedMailboxNameCount())
	for i := 0; i < generatedMailboxNameCount(); i++ {
		name := generatedMailboxName(i)
		if strings.Contains(name, ".") {
			t.Fatalf("generated mailbox base name contains a dot: %q", name)
		}
		names[name] = struct{}{}
	}
	if len(names) < 10_000 {
		t.Fatalf("got %d unique base names, want at least 10000", len(names))
	}
	variants := generatedMailboxVariants("Example.COM")
	if len(variants) != aliasGenerationWindow {
		t.Fatalf("got %d variants, want %d", len(variants), aliasGenerationWindow)
	}
	for _, email := range variants {
		local, domain, ok := splitEmail(email)
		name := strings.TrimRight(local, "0123456789")
		digits := strings.TrimPrefix(local, name)
		if _, known := names[name]; !ok || !known || strings.Contains(name, ".") || domain != "example.com" || len(digits) > 6 {
			t.Fatalf("unexpected generated mailbox %q", email)
		}
	}
}

func TestDomainAllocationTriesAnotherAddressAfterDisabledMailboxConflict(t *testing.T) {
	repo := &generatedMailboxRetryRepo{candidate: DomainCandidate{
		ResourceID: 1, OwnerUserID: 2, Domain: "example.com", MailboxDailyLimit: 10,
	}}
	result, err := NewUseCase(repo).tryDomainCandidate(
		context.Background(),
		AllocateCommand{OrderNo: "order-1", BuyerUserID: 3, SupplyScope: domain.SupplyScopeOwned, ensureOrderGuard: func(context.Context, domain.AllocationType) error { return nil }},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
		repo.candidate,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("tryDomainCandidate() unexpected error: %v", err)
	}
	if repo.calls != 2 || result == nil {
		t.Fatalf("generated mailbox attempts = %d, result = %#v; want two attempts and an allocation", repo.calls, result)
	}
}
