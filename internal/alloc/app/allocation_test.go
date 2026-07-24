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
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
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
	inventoryCalls  int
}

type missingProductInventoryRepoStub struct{ Repository }

func (*missingProductInventoryRepoStub) GetProductInventoryTotals(context.Context, uint) (*ProjectProductInventoryTotals, error) {
	return nil, domain.ErrProjectNotAllocatable
}

func TestProductInventorySnapshotPreservesMissingProjectErrorWithoutCache(t *testing.T) {
	_, err := NewUseCase(&missingProductInventoryRepoStub{}).GetProductInventorySnapshot(context.Background(), 10)
	if !errors.Is(err, domain.ErrProjectNotAllocatable) {
		t.Fatalf("GetProductInventorySnapshot() error = %v", err)
	}
}

type warmOnInitializeInventoryCache struct {
	InventoryCache
	initialized bool
	totals      *ProjectProductInventoryTotals
}

func (c *warmOnInitializeInventoryCache) GetProductInventorySnapshots(_ context.Context, projectIDs []uint) (map[uint]*ProjectProductInventoryTotals, error) {
	result := make(map[uint]*ProjectProductInventoryTotals)
	if c.initialized {
		for _, projectID := range projectIDs {
			result[projectID] = c.totals
		}
	}
	return result, nil
}

func (c *warmOnInitializeInventoryCache) InitializeInventory(context.Context, []InventoryCacheEntry, time.Duration) error {
	c.initialized = true
	return nil
}

func TestProductInventoryColdRaceReturnsConcurrentWarmSnapshot(t *testing.T) {
	cache := &warmOnInitializeInventoryCache{totals: &ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 7}}
	useCase := NewUseCase(&missingProductInventoryRepoStub{})
	useCase.SetInventoryCache(cache)

	totals, err := useCase.GetProductInventorySnapshot(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetProductInventorySnapshot() error = %v", err)
	}
	if totals.Cold || totals.TotalAvailable != 7 {
		t.Fatalf("GetProductInventorySnapshot() = %#v, want concurrent warm snapshot", totals)
	}
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
	q.inventoryCalls++
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
	candidate      DomainCandidate
	reusable       *GeneratedMailboxCandidate
	domainBuckets  []int
	generatedLists int
	calls          int
	consumeCalls   int
	domainCreates  int
}

type allocationLockRepo struct {
	Repository
	config               ProductAllocationConfig
	candidates           []MicrosoftCandidate
	rootUnavailable      map[uint]bool
	candidateUnavailable map[uint]bool
	emptyBuckets         map[uint16]bool
	explicitAlias        *AliasCandidate
	writeConflict        bool
	guardConflict        bool
	finds                int
	lists                int
	listedBuckets        []int
	waiting              int
	skipping             int
	creates              int
	createdResource      uint
	createdMailbox       domain.MicrosoftMailbox
}

func (*allocationLockRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*allocationLockRepo) HasParentTx(context.Context) bool { return true }

func (r *allocationLockRepo) FindExistingAllocation(context.Context, string) (*domain.UnifiedAllocation, error) {
	r.finds++
	return nil, nil
}

func (r *allocationLockRepo) LoadProductConfig(context.Context, uint, uint, bool) (*ProductAllocationConfig, error) {
	if r.config.ProductID == 0 {
		r.config = ProductAllocationConfig{ProjectID: 4, ProductID: 5, ProductType: domain.AllocationTypeMicrosoft, PlusWeight: 1}
	}
	return &r.config, nil
}

func (r *allocationLockRepo) ListMicrosoftSourceCandidates(_ context.Context, _ uint, _ uint, _ domain.SupplyScope, _ domain.MicrosoftMailbox, bucket *uint16, _ int, _ string) ([]MicrosoftCandidate, error) {
	r.lists++
	if bucket == nil {
		r.listedBuckets = append(r.listedBuckets, -1)
	} else {
		r.listedBuckets = append(r.listedBuckets, int(*bucket))
		if r.emptyBuckets[*bucket] {
			return nil, nil
		}
	}
	if len(r.candidates) == 0 {
		return []MicrosoftCandidate{{ResourceID: 1}, {ResourceID: 2}}, nil
	}
	return r.candidates, nil
}

func TestSpecifiedSuffixFallsBackAfterFirstEmptyBucket(t *testing.T) {
	firstBucket := bucketProbeSequence("order-1", 4, string(domain.MicrosoftMailboxPlus), MicrosoftBucketCount)[0]
	tests := []struct {
		name        string
		emailSuffix string
		wantGlobal  bool
	}{
		{name: "specified suffix", emailSuffix: "example.com", wantGlobal: true},
		{name: "random suffix", wantGlobal: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &allocationLockRepo{emptyBuckets: map[uint16]bool{firstBucket: true}}
			result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
				OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: tt.emailSuffix,
			})

			if err != nil || result == nil {
				t.Fatalf("Allocate() result = %#v, error = %v; want success", result, err)
			}
			if len(repo.listedBuckets) != 2 || (repo.listedBuckets[1] == -1) != tt.wantGlobal {
				t.Fatalf("listed buckets = %v, want second query global = %v", repo.listedBuckets, tt.wantGlobal)
			}
		})
	}
}

func (r *allocationLockRepo) LockResourceRoot(context.Context, uint, domain.AllocationType) (bool, error) {
	r.waiting++
	return true, nil
}

func (r *allocationLockRepo) TryLockResourceRoot(_ context.Context, resourceID uint, _ domain.AllocationType) (bool, error) {
	r.skipping++
	return !r.rootUnavailable[resourceID], nil
}

func (r *allocationLockRepo) LockMicrosoftCandidate(_ context.Context, resourceID uint, _ uint, _ uint, _ domain.SupplyScope, _ domain.MicrosoftMailbox, _ string) (*MicrosoftCandidate, error) {
	if r.candidateUnavailable[resourceID] {
		return nil, nil
	}
	candidate := MicrosoftCandidate{ResourceID: resourceID, EmailAddress: fmt.Sprintf("ms%d@example.com", resourceID), PlusDailyLimit: 1}
	for _, item := range r.candidates {
		if item.ResourceID == resourceID {
			candidate = item
			break
		}
	}
	if candidate.EmailAddress == "" {
		candidate.EmailAddress = fmt.Sprintf("ms%d@example.com", resourceID)
	}
	if candidate.PlusDailyLimit == 0 {
		candidate.PlusDailyLimit = 1
	}
	return &candidate, nil
}

func (*allocationLockRepo) EnsureDailyUsageAvailable(_ context.Context, _ string, _ domain.AllocationType, resourceID uint, _ domain.DailyUsageKind, _ int) error {
	if resourceID == 1 {
		return domain.ErrInsufficientInventory
	}
	return nil
}

func (*allocationLockRepo) ConsumeDailyUsage(context.Context, string, domain.AllocationType, uint, domain.DailyUsageKind, int) error {
	return nil
}

func (*allocationLockRepo) FindReusablePlusAlias(_ context.Context, _ uint, resourceID uint) (*AliasCandidate, error) {
	return &AliasCandidate{ID: resourceID, Email: fmt.Sprintf("ms%d+1@example.com", resourceID)}, nil
}

func (r *allocationLockRepo) FindReusableExplicitAlias(context.Context, uint, uint, string) (*AliasCandidate, error) {
	return r.explicitAlias, nil
}

func (*allocationLockRepo) IsMicrosoftMailboxHistoricallyMatched(context.Context, uint, domain.MicrosoftMailbox, uint) (bool, error) {
	return false, nil
}

func (r *allocationLockRepo) CreateOrderGuard(context.Context, string, domain.AllocationType) error {
	if r.guardConflict {
		return domain.ErrAllocationConflict
	}
	return nil
}

func (r *allocationLockRepo) CreateMicrosoftAllocation(_ context.Context, allocation *domain.MicrosoftAllocation) error {
	r.creates++
	r.createdResource = allocation.ResourceID
	r.createdMailbox = allocation.Mailbox
	if r.writeConflict {
		return domain.ErrAllocationConflict
	}
	allocation.ID = uint(r.creates)
	allocation.CreatedAt = time.Now().UTC()
	return nil
}

func (*allocationLockRepo) TouchMicrosoftAllocated(context.Context, uint, time.Time) error {
	return nil
}

func TestAllocationStopsParentTransactionAfterWriteConflict(t *testing.T) {
	repo := &allocationLockRepo{writeConflict: true}
	_, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5,
	})

	if !errors.Is(err, domain.ErrAllocationConflict) {
		t.Fatalf("Allocate() error = %v, want allocation conflict", err)
	}
	if repo.lists != 1 || repo.waiting != 1 || repo.skipping != 1 || repo.creates != 1 {
		t.Fatalf("calls list/wait/skip/create = %d/%d/%d/%d, want 1/1/1/1", repo.lists, repo.waiting, repo.skipping, repo.creates)
	}
}

func TestHistoricalAllocationStopsAfterOrderGuardConflict(t *testing.T) {
	repo := &allocationLockRepo{guardConflict: true}
	_, err := NewUseCase(repo).ImportHistoricalMicrosoftAllocation(context.Background(), HistoricalMicrosoftAllocationCommand{
		ProjectID: 4, ProductID: 5, ResourceID: 1,
		Mailbox: domain.MicrosoftMailboxMain, Email: "main@example.com",
		CreatedAt: time.Now().Add(-time.Hour), ReleasedAt: time.Now(),
	})

	if !errors.Is(err, domain.ErrAllocationConflict) {
		t.Fatalf("ImportHistoricalMicrosoftAllocation() error = %v, want allocation conflict", err)
	}
	if repo.finds != 1 || repo.creates != 0 {
		t.Fatalf("find/create calls = %d/%d, want 1/0", repo.finds, repo.creates)
	}
}

func TestAllocationWaitsForFirstRootAndSkipsLaterCandidateMisses(t *testing.T) {
	tests := []struct {
		name                 string
		rootUnavailable      map[uint]bool
		candidateUnavailable map[uint]bool
	}{
		{name: "busy root", rootUnavailable: map[uint]bool{2: true}},
		{name: "stale candidate", candidateUnavailable: map[uint]bool{2: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &allocationLockRepo{
				candidates:           []MicrosoftCandidate{{ResourceID: 1}, {ResourceID: 2}, {ResourceID: 3}},
				rootUnavailable:      tt.rootUnavailable,
				candidateUnavailable: tt.candidateUnavailable,
			}
			result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
				OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5,
			})

			if err != nil || result == nil || result.ResourceID != 3 {
				t.Fatalf("Allocate() result = %#v, error = %v; want resource 3", result, err)
			}
			if repo.waiting != 1 || repo.skipping != 2 || repo.creates != 1 {
				t.Fatalf("calls wait/skip/create = %d/%d/%d, want 1/2/1", repo.waiting, repo.skipping, repo.creates)
			}
		})
	}
}

func TestAllocationReusesHeldRootAcrossBucketProbes(t *testing.T) {
	repo := &allocationLockRepo{
		candidates:           []MicrosoftCandidate{{ResourceID: 1}},
		candidateUnavailable: map[uint]bool{1: true},
	}

	_, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5,
	})

	if !errors.Is(err, domain.ErrInsufficientInventory) {
		t.Fatalf("Allocate() error = %v, want insufficient inventory", err)
	}
	if repo.waiting != 1 || repo.skipping != 0 {
		t.Fatalf("root lock calls wait/skip = %d/%d, want 1/0", repo.waiting, repo.skipping)
	}
}

func TestMicrosoftMainUsesAliasWhenMainIsAlreadyAllocated(t *testing.T) {
	repo := &allocationLockRepo{
		config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: domain.AllocationTypeMicrosoft, MainWeight: 1,
		},
		candidates:    []MicrosoftCandidate{{ResourceID: 1, EmailAddress: "main@example.com", MainAllocated: true}},
		explicitAlias: &AliasCandidate{ID: 9, Email: "alias@example.com"},
	}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: "example.com",
	})

	if err != nil || result == nil || result.Email != "alias@example.com" {
		t.Fatalf("Allocate() result = %#v, error = %v; want explicit alias", result, err)
	}
	if repo.createdMailbox != domain.MicrosoftMailboxAlias || repo.createdResource != 1 {
		t.Fatalf("created mailbox/resource = %s/%d, want alias/1", repo.createdMailbox, repo.createdResource)
	}
}

func TestMicrosoftAllocationRejectsWrongDeliverySuffix(t *testing.T) {
	repo := &allocationLockRepo{
		config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: domain.AllocationTypeMicrosoft, MainWeight: 1,
		},
		candidates:    []MicrosoftCandidate{{ResourceID: 1, EmailAddress: "main@hotmail.com", MainAllocated: true}},
		explicitAlias: &AliasCandidate{ID: 9, Email: "alias@outlook.com"},
	}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: "hotmail.com",
	})

	if !errors.Is(err, domain.ErrInsufficientInventory) || result != nil {
		t.Fatalf("Allocate() result = %#v, error = %v; want insufficient inventory", result, err)
	}
	if repo.creates != 0 {
		t.Fatalf("CreateMicrosoftAllocation() calls = %d, want 0", repo.creates)
	}
}

func (*generatedMailboxRetryRepo) LockResourceRoot(context.Context, uint, domain.AllocationType) (bool, error) {
	return true, nil
}

func (r *generatedMailboxRetryRepo) LockDomainCandidate(context.Context, uint, uint, domain.SupplyScope, string) (*DomainCandidate, error) {
	return &r.candidate, nil
}

func (r *generatedMailboxRetryRepo) ListDomainSourceCandidates(_ context.Context, _ uint, _ domain.SupplyScope, bucket *uint16, _ int, _ string) ([]DomainCandidate, error) {
	if bucket == nil {
		r.domainBuckets = append(r.domainBuckets, -1)
	} else {
		r.domainBuckets = append(r.domainBuckets, int(*bucket))
	}
	return []DomainCandidate{r.candidate}, nil
}

func (r *generatedMailboxRetryRepo) ListGeneratedMailboxCandidates(context.Context, uint, uint, domain.SupplyScope, *uint16, int, string) ([]GeneratedMailboxCandidate, error) {
	r.generatedLists++
	if r.reusable == nil {
		return nil, nil
	}
	return []GeneratedMailboxCandidate{*r.reusable}, nil
}

func (r *generatedMailboxRetryRepo) LockGeneratedMailboxCandidate(context.Context, uint, uint, uint) (*GeneratedMailboxCandidate, error) {
	return r.reusable, nil
}

func (*generatedMailboxRetryRepo) EnsureDailyUsageAvailable(context.Context, string, domain.AllocationType, uint, domain.DailyUsageKind, int) error {
	return nil
}

func (*generatedMailboxRetryRepo) IsDomainMailboxAllocated(context.Context, uint, uint) (bool, error) {
	return false, nil
}

func (r *generatedMailboxRetryRepo) FindReusableGeneratedMailbox(context.Context, uint, uint) (*GeneratedMailboxCandidate, error) {
	return r.reusable, nil
}

func (r *generatedMailboxRetryRepo) FindOrCreateGeneratedMailbox(_ context.Context, _ uint, _ uint, email string) (*GeneratedMailboxCandidate, error) {
	r.calls++
	if r.calls == 1 {
		return nil, nil
	}
	return &GeneratedMailboxCandidate{ID: 7, Email: email}, nil
}

func (r *generatedMailboxRetryRepo) CreateDomainAllocation(_ context.Context, allocation *domain.GeneratedMailboxAllocation) error {
	r.domainCreates++
	allocation.ID = 8
	allocation.CreatedAt = time.Now().UTC()
	return nil
}

func (r *generatedMailboxRetryRepo) ConsumeDailyUsage(context.Context, string, domain.AllocationType, uint, domain.DailyUsageKind, int) error {
	r.consumeCalls++
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

func TestDotAliasVariantsSkipPositionsAdjacentToExistingDots(t *testing.T) {
	want := []string{
		"m.s.1000@example.com",
		"m.s1.000@example.com",
		"m.s10.00@example.com",
		"m.s100.0@example.com",
	}
	if got := dotAliasVariants("m.s1000@example.com"); !slices.Equal(got, want) {
		t.Fatalf("dotAliasVariants() = %v, want %v", got, want)
	}
}

func TestAllocationRuntimeSettingsApplyToNewWork(t *testing.T) {
	settings := map[string]string{
		"candidate_window_size":                "2",
		"global_candidate_window":              "3",
		"bucket_probe_count":                   "2",
		"alias_generation_window":              "3",
		"candidate_retry_count":                "2",
		"dot_alias_capacity_per_resource":      "2",
		"inventory_refresh_interval_minutes":   "4",
		"inventory_cache_activity_ttl_minutes": "5",
		"inventory_cache_hard_ttl_hours":       "6",
	}
	for key, value := range settings {
		runtimeconfig.Set(key, value)
		defer runtimeconfig.Delete(key)
	}

	if candidateWindowSizeValue() != 2 || globalCandidateWindowValue() != 3 || bucketProbeCountValue() != 2 || candidateRetryCountValue() != 2 {
		t.Fatal("candidate runtime settings were not applied")
	}
	if got := len(plusAliasVariants("user@example.com", 1, "order")); got != 3 {
		t.Fatalf("got %d generated aliases, want 3", got)
	}
	if got := len(dotAliasVariants("username@example.com")); got != 2 {
		t.Fatalf("got %d dot aliases, want 2", got)
	}
	if InventoryRefreshIntervalValue() != 4*time.Minute || inventoryCacheActivityTTLValue() != 5*time.Minute || inventoryCacheHardTTLValue() != 6*time.Hour {
		t.Fatal("inventory runtime settings were not applied")
	}
}

func TestAllocationRuntimeSettingsClampUnsafeValues(t *testing.T) {
	settings := map[string]string{
		"candidate_window_size":                "2147483647",
		"global_candidate_window":              "2147483647",
		"bucket_probe_count":                   "2147483647",
		"alias_generation_window":              "2147483647",
		"candidate_retry_count":                "2147483647",
		"dot_alias_capacity_per_resource":      "2147483647",
		"inventory_refresh_interval_minutes":   "1000000",
		"inventory_cache_activity_ttl_minutes": "1000000",
		"inventory_cache_hard_ttl_hours":       "100000",
	}
	for key, value := range settings {
		runtimeconfig.Set(key, value)
		defer runtimeconfig.Delete(key)
	}

	if candidateWindowSizeValue() != maxCandidateWindowSize || globalCandidateWindowValue() != maxCandidateWindowSize {
		t.Fatal("candidate windows were not clamped")
	}
	if bucketProbeCountValue() != maxBucketProbeCount || aliasGenerationWindowValue() != maxAliasGenerationWindow || candidateRetryCountValue() != maxCandidateRetryCount {
		t.Fatal("allocation loop bounds were not clamped")
	}
	if DotAliasCapacityPerResourceValue() != maxDotAliasCapacity {
		t.Fatal("dot alias capacity was not clamped")
	}
	if InventoryRefreshIntervalValue() != maxInventoryRefreshInterval || inventoryCacheActivityTTLValue() != maxInventoryCacheActivityTTL || inventoryCacheHardTTLValue() != maxInventoryCacheHardTTL {
		t.Fatal("inventory durations were not clamped")
	}
}

func TestBucketProbeSequenceSupportsExpandedBuckets(t *testing.T) {
	runtimeconfig.Set("bucket_probe_count", "64")
	defer runtimeconfig.Delete("bucket_probe_count")

	for name, test := range map[string]struct {
		kind  string
		count uint16
	}{
		"microsoft": {kind: string(domain.MicrosoftMailboxPlus), count: MicrosoftBucketCount},
		"domain":    {kind: "domain", count: DomainBucketCount},
		"generated": {kind: "generated-domain", count: GeneratedMailboxBucketCount},
	} {
		t.Run(name, func(t *testing.T) {
			seenAboveUint8 := false
			for i := 0; i < 128; i++ {
				buckets := bucketProbeSequence(fmt.Sprintf("order-%d", i), 4, test.kind, test.count)
				if len(buckets) != maxBucketProbeCount {
					t.Fatalf("bucket count = %d, want %d", len(buckets), maxBucketProbeCount)
				}
				for _, bucket := range buckets {
					if bucket >= test.count {
						t.Fatalf("bucket = %d, want < %d", bucket, test.count)
					}
					seenAboveUint8 = seenAboveUint8 || bucket > 255
				}
			}
			if !seenAboveUint8 {
				t.Fatal("probe sequence never used a bucket above uint8 capacity")
			}
		})
	}
}

func TestAllocationMetricResultClassifiesOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		existing bool
		want     string
	}{
		{name: "success", want: "succeeded"},
		{name: "existing idempotent allocation", existing: true, want: "existing"},
		{name: "existing commit failure", err: errors.New("commit failed"), existing: true, want: "system_failed"},
		{name: "insufficient inventory", err: domain.ErrInsufficientInventory, want: "insufficient_inventory"},
		{name: "conflict", err: domain.ErrAllocationConflict, want: "conflict"},
		{name: "invalid request", err: domain.ErrInvalidAllocationRequest, want: "invalid_request"},
		{name: "project unavailable", err: domain.ErrProjectNotAllocatable, want: "invalid_request"},
		{name: "system failure", err: errors.New("database unavailable"), want: "system_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allocationMetricResult(tt.err, tt.existing); got != tt.want {
				t.Fatalf("allocationMetricResult(%v, %t) = %q, want %q", tt.err, tt.existing, got, tt.want)
			}
		})
	}
}

func TestDomainAllocationReusesBucketedGeneratedMailboxBeforeCreating(t *testing.T) {
	repo := &generatedMailboxRetryRepo{
		candidate: DomainCandidate{ResourceID: 1, OwnerUserID: 2, Domain: "example.com", MailboxDailyLimit: 10},
		reusable:  &GeneratedMailboxCandidate{ID: 7, ResourceID: 1, Email: "existing@example.com"},
	}
	result, busy, err := NewUseCase(repo).tryReusableDomainMailboxes(
		context.Background(),
		AllocateCommand{OrderNo: "order-1", BuyerUserID: 3, SupplyScope: domain.SupplyScopeOwned, ensureOrderGuard: func(context.Context, domain.AllocationType) error { return nil }},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
	)
	if err != nil || busy || result == nil || result.Email != "existing@example.com" {
		t.Fatalf("reuse result = %#v, busy = %v, error = %v", result, busy, err)
	}
	if repo.calls != 0 {
		t.Fatalf("generated mailbox calls = %d, want 0", repo.calls)
	}
}

func TestSpecifiedDomainReusesMailboxWithoutMailboxBucketProbe(t *testing.T) {
	repo := &generatedMailboxRetryRepo{
		candidate: DomainCandidate{ResourceID: 1, OwnerUserID: 2, Domain: "example.com", MailboxDailyLimit: 10},
		reusable:  &GeneratedMailboxCandidate{ID: 7, ResourceID: 1, Email: "existing@example.com"},
	}
	result, err := NewUseCase(repo).allocateDomainOnce(
		context.Background(),
		AllocateCommand{OrderNo: "order-1", BuyerUserID: 3, SupplyScope: domain.SupplyScopeOwned, EmailSuffix: "example.com", ensureOrderGuard: func(context.Context, domain.AllocationType) error { return nil }},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
	)
	if err != nil || result == nil || result.Email != "existing@example.com" {
		t.Fatalf("specified-domain reuse result = %#v, error = %v", result, err)
	}
	if repo.generatedLists != 0 || len(repo.domainBuckets) != 1 || repo.domainBuckets[0] != -1 || repo.calls != 0 {
		t.Fatalf("generated lists/domain buckets/generated calls = %d/%v/%d, want 0/[-1]/0", repo.generatedLists, repo.domainBuckets, repo.calls)
	}
}

func TestDomainAllocationRejectsWrongDeliverySuffixBeforeUsage(t *testing.T) {
	repo := &generatedMailboxRetryRepo{}
	result, err := NewUseCase(repo).createDomainAllocation(
		context.Background(),
		AllocateCommand{EmailSuffix: "example.com", ensureOrderGuard: func(context.Context, domain.AllocationType) error { return nil }},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
		1,
		7,
		"wrong@other.com",
		time.Now().UTC(),
		&DailyUsageReservation{UsageDate: "2026-07-25", AllocationType: domain.AllocationTypeDomain, ResourceID: 1, Kind: domain.DailyUsageKindDomainMailbox, Limit: 10},
	)
	if !errors.Is(err, errCandidateUnavailable) || result != nil {
		t.Fatalf("wrong-suffix result = %#v, error = %v", result, err)
	}
	if repo.consumeCalls != 0 || repo.domainCreates != 0 {
		t.Fatalf("usage/create calls = %d/%d, want 0/0", repo.consumeCalls, repo.domainCreates)
	}
}

func TestDomainAllocationTriesAnotherAddressAfterDisabledMailbox(t *testing.T) {
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
