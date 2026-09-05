package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type missingProductInventoryRepoStub struct{ Repository }

func (*missingProductInventoryRepoStub) GetProductInventoryTotals(context.Context, uint) (*ProjectProductInventoryTotals, error) {
	return nil, domain.ErrProjectNotAllocatable
}

type directProductInventoryRepoStub struct {
	Repository
	totals *ProjectProductInventoryTotals
}

func (r *directProductInventoryRepoStub) GetProductInventoryTotals(context.Context, uint) (*ProjectProductInventoryTotals, error) {
	return r.totals, nil
}

func TestProductInventorySnapshotDatesAuthoritativeDBFallback(t *testing.T) {
	useCase := NewUseCase(&directProductInventoryRepoStub{
		totals: &ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 7},
	})
	before := time.Now().UTC()

	totals, err := useCase.GetProductInventorySnapshot(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetProductInventorySnapshot() error = %v", err)
	}
	if totals.RefreshedAt == nil || totals.RefreshedAt.Before(before) || totals.RefreshedAt.After(time.Now().UTC()) {
		t.Fatalf("RefreshedAt = %v, want authoritative query time", totals.RefreshedAt)
	}
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

type generatedMailboxRetryRepo struct {
	Repository
	candidate       DomainCandidate
	reusable        *GeneratedMailboxCandidate
	domainBuckets   []int
	generatedLists  int
	calls           int
	historyChecks   int
	historyEmails   []string
	historicalFirst bool
	consumeCalls    int
	domainCreates   int
}

type allocationLockRepo struct {
	Repository
	config               ProductAllocationConfig
	suffixInventory      map[string]int64
	candidates           []MicrosoftCandidate
	rootUnavailable      map[uint]bool
	candidateUnavailable map[uint]bool
	emptyBuckets         map[uint16]bool
	noCandidates         bool
	explicitAlias        *AliasCandidate
	writeConflict        bool
	guardConflict        bool
	finds                int
	lists                int
	listedBuckets        []int
	listedSuffixes       []string
	waiting              int
	skipping             int
	creates              int
	createdResource      uint
	createdMailbox       domain.MicrosoftMailbox
	txActive             bool
	suffixInventoryInTx  bool
	suffixInventoryCalls int
}

func (r *allocationLockRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	r.txActive = true
	defer func() { r.txActive = false }()
	return fn(ctx)
}

func (*allocationLockRepo) HasParentTx(context.Context) bool { return true }

func (r *allocationLockRepo) FindExistingAllocation(context.Context, string) (*domain.UnifiedAllocation, error) {
	r.finds++
	return nil, nil
}

func (r *allocationLockRepo) LoadProductConfig(context.Context, uint, uint, bool) (*ProductAllocationConfig, error) {
	if r.config.ProductID == 0 {
		r.config = ProductAllocationConfig{ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft, PlusWeight: 1}
	}
	return &r.config, nil
}

func (r *allocationLockRepo) ListProductSuffixInventory(context.Context, ProductAllocationConfig, uint, domain.SupplyScope) (map[string]int64, error) {
	r.suffixInventoryCalls++
	r.suffixInventoryInTx = r.suffixInventoryInTx || r.txActive
	return r.suffixInventory, nil
}

func (r *allocationLockRepo) ListMicrosoftSourceCandidates(_ context.Context, _ uint, _ uint, _ domain.SupplyScope, _ domain.MicrosoftMailbox, bucket *uint16, _ int, emailSuffix string) ([]MicrosoftCandidate, error) {
	r.lists++
	r.listedSuffixes = append(r.listedSuffixes, emailSuffix)
	if bucket == nil {
		r.listedBuckets = append(r.listedBuckets, -1)
	} else {
		r.listedBuckets = append(r.listedBuckets, int(*bucket))
		if r.emptyBuckets[*bucket] {
			return nil, nil
		}
	}
	if r.noCandidates {
		return nil, nil
	}
	if len(r.candidates) == 0 {
		return []MicrosoftCandidate{{ResourceID: 1}, {ResourceID: 2}}, nil
	}
	return r.candidates, nil
}

func TestChooseWeightedInventorySuffixUsesAvailableCounts(t *testing.T) {
	inventory := map[string]int64{"outlook.com": 1, "outlook.fr": 3, "ignored.example": 100}
	for _, test := range []struct {
		ticket int64
		want   string
	}{
		{ticket: 0, want: "outlook.com"},
		{ticket: 1, want: "outlook.fr"},
		{ticket: 3, want: "outlook.fr"},
	} {
		got, ok := chooseWeightedInventorySuffix(inventory, func(suffix string) bool {
			return suffix != "ignored.example"
		}, func(total int64) int64 {
			if total != 4 {
				t.Fatalf("total weight = %d, want 4", total)
			}
			return test.ticket
		})
		if !ok || got != test.want {
			t.Fatalf("ticket %d selected %q, %t; want %q", test.ticket, got, ok, test.want)
		}
	}
}

func TestOutlookSelectorChoosesInStockWhitelistedSuffix(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()["microsoft_domain_whitelist"]
	runtimeconfig.Set("microsoft_domain_whitelist", "outlook.com,hotmail.com")
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set("microsoft_domain_whitelist", previous)
		} else {
			runtimeconfig.Delete("microsoft_domain_whitelist")
		}
	})

	repo := &allocationLockRepo{
		config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft, MainWeight: 1,
		},
		suffixInventory: map[string]int64{"not-microsoft.example": 100, "hotmail.com": 1},
		candidates:      []MicrosoftCandidate{{ResourceID: 2, EmailAddress: "available@hotmail.com"}},
	}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-random-outlook", BuyerUserID: 3, ProjectProductID: 5,
		SupplyScope: domain.SupplyScopePublic, EmailSuffix: coredomain.RandomMicrosoftSuffixSelector,
	})
	if err != nil || result == nil || result.Email != "available@hotmail.com" {
		t.Fatalf("Allocate() result = %#v, error = %v; want hotmail allocation", result, err)
	}
	for _, suffix := range repo.listedSuffixes {
		if suffix != "hotmail.com" {
			t.Fatalf("candidate suffix = %q, want hotmail.com", suffix)
		}
	}
	if repo.suffixInventoryCalls != 1 || repo.suffixInventoryInTx {
		t.Fatalf("suffix inventory calls/in transaction = %d/%t, want 1/false", repo.suffixInventoryCalls, repo.suffixInventoryInTx)
	}
}

func TestRandomPublicSuffixSelectionUsesInventorySnapshot(t *testing.T) {
	repo := &allocationLockRepo{config: ProductAllocationConfig{
		ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft, MainWeight: 1,
	}}
	cache := &warmOnInitializeInventoryCache{
		initialized: true,
		totals: &ProjectProductInventoryTotals{ProjectID: 4, Items: []ProductInventoryTotal{{
			ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft,
			Suffixes: []ProductInventorySuffixTotal{{Suffix: "outlook.com", PublicAvailable: 7}},
		}}},
	}
	useCase := NewUseCase(repo)
	useCase.SetInventoryCache(cache)

	suffix, err := useCase.SelectRandomInventorySuffix(context.Background(), ProductSuffixSelectionRequest{
		ProjectID: 4, ProductID: 5, BuyerUserID: 3,
		SupplyScopes: []domain.SupplyScope{domain.SupplyScopePublic},
		Selector:     coredomain.RandomMicrosoftSuffixSelector,
	})

	if err != nil || suffix != "outlook.com" {
		t.Fatalf("selected suffix = %q, error = %v; want outlook.com", suffix, err)
	}
	if repo.suffixInventoryCalls != 0 {
		t.Fatalf("authoritative suffix inventory calls = %d, want cached public snapshot", repo.suffixInventoryCalls)
	}
}

func TestDomainSelectorChoosesInStockSuffix(t *testing.T) {
	repo := &allocationLockRepo{suffixInventory: map[string]int64{"com.cn": 2}}
	suffix, err := NewUseCase(repo).selectRandomInventorySuffix(
		context.Background(),
		ProductAllocationConfig{ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeDomain},
		3,
		[]domain.SupplyScope{domain.SupplyScopePublic},
		coredomain.RandomDomainSuffixSelector,
	)
	if err != nil || suffix != "com.cn" {
		t.Fatalf("selected suffix = %q, error = %v; want com.cn", suffix, err)
	}
}

func TestRandomSuffixInventoryPrecheckUsesMatchingProductSuffixes(t *testing.T) {
	totals := &ProjectProductInventoryTotals{Items: []ProductInventoryTotal{
		{
			ProductID: 1, ProductType: coredomain.ProductTypeMicrosoft,
			Suffixes: []ProductInventorySuffixTotal{{Suffix: "outlook.com", PublicAvailable: 2}},
		},
		{
			ProductID: 2, ProductType: coredomain.ProductTypeDomain,
			Suffixes: []ProductInventorySuffixTotal{{Suffix: "com", PublicAvailable: 3}},
		},
	}}
	for _, test := range []struct {
		productID uint
		selector  string
	}{
		{productID: 1, selector: coredomain.RandomMicrosoftSuffixSelector},
		{productID: 2, selector: coredomain.RandomDomainSuffixSelector},
	} {
		available, known := productInventoryAvailable(totals, ProductInventoryAvailabilityRequest{
			ProductID: test.productID, EmailSuffix: test.selector, PublicOnly: true,
		})
		if !known || !available {
			t.Fatalf("product %d selector %q availability = %t, %t; want true, true", test.productID, test.selector, available, known)
		}
	}
}

func TestSpecifiedMicrosoftSuffixMissingFromSnapshotIsUnknown(t *testing.T) {
	totals := &ProjectProductInventoryTotals{Items: []ProductInventoryTotal{{
		ProductID: 1, ProductType: coredomain.ProductTypeMicrosoft,
		Suffixes: []ProductInventorySuffixTotal{{Suffix: "hotmail.com", PublicAvailable: 2}},
	}}}
	available, known := productInventoryAvailable(totals, ProductInventoryAvailabilityRequest{
		ProductID: 1, EmailSuffix: "outlook.com", PublicOnly: true,
	})
	if available || known {
		t.Fatalf("explicit Microsoft suffix availability = %t, %t; want false, false", available, known)
	}
}

func TestSpecifiedSuffixProbesEveryBucketBeforeGlobalFallback(t *testing.T) {
	buckets := bucketProbeSequence("order-1", 4, string(domain.MicrosoftMailboxPlus), MicrosoftBucketCount)
	emptyBuckets := make(map[uint16]bool, len(buckets))
	wantBuckets := make([]int, 0, len(buckets)+1)
	for _, bucket := range buckets {
		emptyBuckets[bucket] = true
		wantBuckets = append(wantBuckets, int(bucket))
	}
	wantBuckets = append(wantBuckets, -1)

	repo := &allocationLockRepo{emptyBuckets: emptyBuckets}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: "example.com",
	})

	if err != nil || result == nil {
		t.Fatalf("Allocate() result = %#v, error = %v; want success", result, err)
	}
	if !slices.Equal(repo.listedBuckets, wantBuckets) {
		t.Fatalf("listed buckets = %v, want all configured probes then global %v", repo.listedBuckets, wantBuckets)
	}
}

func TestMicrosoftEmptyGlobalFallbackIsDefinitive(t *testing.T) {
	repo := &allocationLockRepo{
		config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft, MainWeight: 1,
		},
		noCandidates: true,
	}

	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-empty", BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: "missing.example",
	})

	if result != nil || !errors.Is(err, domain.ErrDefinitiveInventoryExhausted) {
		t.Fatalf("Allocate() result = %#v, error = %v; want definitive exhaustion", result, err)
	}
	if repo.lists != bucketProbeCount+1 {
		t.Fatalf("candidate queries = %d, want %d bucket probes plus one global confirmation", repo.lists, bucketProbeCount+1)
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
	if errors.Is(err, domain.ErrDefinitiveInventoryExhausted) {
		t.Fatalf("Allocate() error = %v, candidate recheck misses must stay bounded/non-definitive", err)
	}
	if repo.waiting != 1 || repo.skipping != 0 {
		t.Fatalf("root lock calls wait/skip = %d/%d, want 1/0", repo.waiting, repo.skipping)
	}
}

func TestMicrosoftMainUsesAliasWhenMainIsAlreadyAllocated(t *testing.T) {
	repo := &allocationLockRepo{
		config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft, MainWeight: 1,
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
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft, MainWeight: 1,
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

func (r *generatedMailboxRetryRepo) IsDomainEmailHistoricallyAllocated(_ context.Context, _ uint, email string) (bool, error) {
	r.historyChecks++
	r.historyEmails = append(r.historyEmails, email)
	return r.historicalFirst && r.historyChecks == 1, nil
}

func (r *generatedMailboxRetryRepo) FindReusableGeneratedMailbox(context.Context, uint, uint) (*GeneratedMailboxCandidate, error) {
	return r.reusable, nil
}

func (r *generatedMailboxRetryRepo) FindOrCreateGeneratedMailbox(_ context.Context, _ uint, _ uint, email string) (*GeneratedMailboxCandidate, error) {
	r.calls++
	if r.calls == 1 {
		return nil, nil
	}
	return &GeneratedMailboxCandidate{ID: uint(5 + r.calls), Email: email}, nil
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

func TestGmailDotAliasVariantsCoverEveryDotCombination(t *testing.T) {
	want := map[string]bool{
		"abc@gmail.com":        true,
		"ab.c@gmail.com":       true,
		"a.b.c@gmail.com":      true,
		"a.bc@googlemail.com":  true,
		"abc@googlemail.com":   true,
		"ab.c@googlemail.com":  true,
		"a.b.c@googlemail.com": true,
	}
	for _, source := range []string{"a.bc@Gmail.com", "a.bc@googlemail.com"} {
		remaining := maps.Clone(want)
		got := gmailDotAliasVariants(source, 0)
		if len(got) != len(remaining) {
			t.Fatalf("gmailDotAliasVariants(%q) returned %d aliases, want %d: %v", source, len(got), len(remaining), got)
		}
		for _, alias := range got {
			if !remaining[alias] {
				t.Fatalf("gmailDotAliasVariants(%q) returned unexpected alias %q", source, alias)
			}
			delete(remaining, alias)
		}
	}
	if got := gmailPrimaryAddress("User.Name@googlemail.com"); got != "user.name@gmail.com" {
		t.Fatalf("gmailPrimaryAddress() = %q, want user.name@gmail.com", got)
	}
}

func TestGmailDotAliasCapacitySupportsMaximumLocalPart(t *testing.T) {
	if got, want := gmailDotAliasCapacity(strings.Repeat("a", GmailDotMaxLocalCharacters)+"@gmail.com"), uint64(1<<30)-1; got != want {
		t.Fatalf("maximum Gmail dot capacity = %d, want %d", got, want)
	}
	if got := gmailDotAliasCapacity(strings.Repeat("a", GmailDotMaxLocalCharacters+1) + "@gmail.com"); got != 0 {
		t.Fatalf("overlong Gmail dot capacity = %d, want 0", got)
	}
}

func TestGmailMailboxPreferencesAreFixedByProduct(t *testing.T) {
	main := gmailMailboxPreferences("main-order", ProductAllocationConfig{
		ProductType: coredomain.ProductTypeGmail, PlusWeight: 100,
	})
	if want := []domain.GmailMailbox{domain.GmailMailboxMain}; !slices.Equal(main, want) {
		t.Fatalf("gmail preferences = %v, want %v", main, want)
	}
	firstKinds := map[domain.GmailMailbox]bool{}
	for i := 0; i < 100; i++ {
		variant := gmailMailboxPreferences(fmt.Sprintf("special-order-%d", i), ProductAllocationConfig{
			ProductType: coredomain.ProductTypeGmailVariant,
		})
		if len(variant) != 2 || variant[0] == variant[1] ||
			!slices.Contains(variant, domain.GmailMailboxDot) || !slices.Contains(variant, domain.GmailMailboxPlus) {
			t.Fatalf("gmail variant preferences = %v, want dot and plus", variant)
		}
		firstKinds[variant[0]] = true
	}
	if !firstKinds[domain.GmailMailboxDot] || !firstKinds[domain.GmailMailboxPlus] {
		t.Fatalf("gmail variant first choices = %v, want balanced dot and plus choices", firstKinds)
	}
}

type gmailAllocationTestRepo struct {
	Repository
	candidates     []GmailCandidate
	busyRoots      map[uint]bool
	historyCount   uint64
	unavailable    map[string]struct{}
	productType    coredomain.ProductType
	tryLocks       []uint
	waitLocks      int
	historyBatches int
}

func (r *gmailAllocationTestRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*gmailAllocationTestRepo) HasParentTx(context.Context) bool { return true }

func (*gmailAllocationTestRepo) FindExistingAllocation(context.Context, string) (*domain.UnifiedAllocation, error) {
	return nil, nil
}

func (r *gmailAllocationTestRepo) LoadProductConfig(context.Context, uint, uint, bool) (*ProductAllocationConfig, error) {
	productType := r.productType
	if productType == "" {
		productType = coredomain.ProductTypeGmail
	}
	return &ProductAllocationConfig{
		ProjectID: 10, ProductID: 20, ProductType: productType,
		CodeEnabled: true, CodeSupplierPrice: "0",
	}, nil
}

func (r *gmailAllocationTestRepo) ListGmailSourceCandidates(_ context.Context, _ uint, _ uint, _ domain.SupplyScope, _ domain.GmailMailbox, after *GmailCandidate, limit int) ([]GmailCandidate, error) {
	start := 0
	if after != nil {
		for start < len(r.candidates) && r.candidates[start].ResourceID != after.ResourceID {
			start++
		}
		start++
	}
	end := min(start+limit, len(r.candidates))
	return r.candidates[start:end], nil
}

func (r *gmailAllocationTestRepo) LockResourceRoot(context.Context, uint, domain.AllocationType) (bool, error) {
	r.waitLocks++
	return true, nil
}

func (r *gmailAllocationTestRepo) TryLockResourceRoot(_ context.Context, resourceID uint, _ domain.AllocationType) (bool, error) {
	r.tryLocks = append(r.tryLocks, resourceID)
	return !r.busyRoots[resourceID], nil
}

func (r *gmailAllocationTestRepo) LockGmailCandidate(_ context.Context, resourceID uint, _ uint, _ uint, _ domain.SupplyScope, _ domain.GmailMailbox) (*GmailCandidate, error) {
	for _, candidate := range r.candidates {
		if candidate.ResourceID == resourceID {
			result := candidate
			return &result, nil
		}
	}
	return nil, nil
}

func (r *gmailAllocationTestRepo) CountGmailDotHistory(context.Context, uint, uint) (uint64, error) {
	return r.historyCount, nil
}

func (r *gmailAllocationTestRepo) ListUnavailableGmailMailboxEmails(_ context.Context, _ uint, _ domain.GmailMailbox, emails []string) (map[string]struct{}, error) {
	r.historyBatches++
	result := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		if _, exists := r.unavailable[email]; exists {
			result[email] = struct{}{}
		}
	}
	return result, nil
}

func (*gmailAllocationTestRepo) IsGmailMailboxAvailable(context.Context, uint, uint, domain.GmailMailbox, string) (bool, error) {
	return true, nil
}

func (*gmailAllocationTestRepo) CreateOrderGuard(context.Context, string, domain.AllocationType) error {
	return nil
}

func (r *gmailAllocationTestRepo) CreateGmailAllocation(_ context.Context, allocation *domain.GmailAllocation) error {
	allocation.ID = 1
	return nil
}

func (*gmailAllocationTestRepo) TouchGmailAllocated(context.Context, uint, time.Time) error {
	return nil
}

func TestGmailAllocationPagesPastBusyFirstWindow(t *testing.T) {
	window := globalCandidateWindowValue()
	repo := &gmailAllocationTestRepo{
		candidates: make([]GmailCandidate, 0, window+1),
		busyRoots:  make(map[uint]bool, window),
	}
	wantLocks := make([]uint, 0, window+1)
	for i := 1; i <= window+1; i++ {
		resourceID := uint(i)
		repo.candidates = append(repo.candidates, GmailCandidate{ResourceID: resourceID, Email: fmt.Sprintf("user%d@gmail.com", i)})
		wantLocks = append(wantLocks, resourceID)
		if i <= window {
			repo.busyRoots[resourceID] = true
		}
	}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "gmail-rotate", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic, ServiceMode: domain.GmailServiceModeCode,
	})
	if err != nil || result == nil || result.ResourceID != uint(window+1) {
		t.Fatalf("Allocate() result = %#v, error = %v; want resource %d", result, err, window+1)
	}
	if repo.waitLocks != 0 || !slices.Equal(repo.tryLocks, wantLocks) {
		t.Fatalf("wait/try root locks = %d/%v, want 0/%v", repo.waitLocks, repo.tryLocks, wantLocks)
	}
}

func TestGmailDotAllocationScansPastFragmentedHistoryWindow(t *testing.T) {
	email := "abcdefghijkl@gmail.com"
	window := aliasGenerationWindowValue()
	blocked := gmailDotAliasVariantBatch(email, uint64(window), window)
	unavailable := make(map[string]struct{}, len(blocked))
	for _, alias := range blocked {
		unavailable[alias] = struct{}{}
	}
	repo := &gmailAllocationTestRepo{
		candidates:   []GmailCandidate{{ResourceID: 1, Email: email}},
		historyCount: uint64(len(blocked)),
		unavailable:  unavailable,
		productType:  coredomain.ProductTypeGmailVariant,
	}
	orderNo := ""
	for i := 0; i < 100; i++ {
		candidate := fmt.Sprintf("gmail-fragmented-%d", i)
		if gmailMailboxPreferences(candidate, ProductAllocationConfig{ProductType: coredomain.ProductTypeGmailVariant})[0] == domain.GmailMailboxDot {
			orderNo = candidate
			break
		}
	}
	if orderNo == "" {
		t.Fatal("no deterministic Gmail dot preference found")
	}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: orderNo, BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic, ServiceMode: domain.GmailServiceModeCode,
	})
	if err != nil || result == nil {
		t.Fatalf("Allocate() result = %#v, error = %v; want free alias after fragmented window", result, err)
	}
	if _, blocked := unavailable[result.Email]; blocked || repo.historyBatches != 2 {
		t.Fatalf("allocated email/batches = %q/%d, want unblocked alias from second batch", result.Email, repo.historyBatches)
	}
}

func TestGmailPlusAliasVariantsUseShortAlphanumericSuffixes(t *testing.T) {
	runtimeconfig.Set("alias_generation_window", "32")
	t.Cleanup(func() { runtimeconfig.Delete("alias_generation_window") })

	variants := gmailPlusAliasVariants("User.Name@GMAIL.COM", "gmail-plus-domains")
	if len(variants) != 32 {
		t.Fatalf("got %d Gmail plus aliases, want 32", len(variants))
	}
	seen := make(map[string]struct{}, len(variants))
	domains := make(map[string]int, 2)
	for _, email := range variants {
		parts := strings.SplitN(email, "@", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid Gmail plus alias %q", email)
		}
		local, domainPart := parts[0], strings.ToLower(parts[1])
		plus := strings.IndexByte(local, '+')
		if plus <= 0 || plus == len(local)-1 || domainPart != "gmail.com" && domainPart != "googlemail.com" {
			t.Fatalf("invalid Gmail plus alias %q", email)
		}
		domains[domainPart]++
		suffix := local[plus+1:]
		if len(suffix) < 4 || len(suffix) > 12 {
			t.Fatalf("Gmail plus suffix length = %d, want 4..12: %q", len(suffix), email)
		}
		hasLetter, hasDigit := false, false
		for _, character := range suffix {
			switch {
			case character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z':
				hasLetter = true
			case character >= '0' && character <= '9':
				hasDigit = true
			default:
				t.Fatalf("Gmail plus suffix contains non-alphanumeric character: %q", email)
			}
		}
		if !hasLetter || !hasDigit {
			t.Fatalf("Gmail plus suffix must contain letters and digits: %q", email)
		}
		if _, exists := seen[email]; exists {
			t.Fatalf("duplicate Gmail plus alias %q", email)
		}
		seen[email] = struct{}{}
	}
	if domains["gmail.com"] != 16 || domains["googlemail.com"] != 16 {
		t.Fatalf("Gmail plus domains = %v, want 16 aliases on each equivalent domain", domains)
	}
}

type historicalGmailReplayRepo struct {
	Repository
	existing domain.UnifiedAllocation
}

func (r *historicalGmailReplayRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (r *historicalGmailReplayRepo) LockResourceRoot(context.Context, uint, domain.AllocationType) (bool, error) {
	return true, nil
}

func (r *historicalGmailReplayRepo) FindExistingAllocation(context.Context, string) (*domain.UnifiedAllocation, error) {
	result := r.existing
	return &result, nil
}

func TestHistoricalGmailAllocationReplayPreservesOriginalProduct(t *testing.T) {
	createdAt := time.Now().UTC().Add(-time.Hour)
	cmd := HistoricalGmailAllocationCommand{
		ProjectID: 10, ProductID: 21, ResourceID: 30, Mailbox: domain.GmailMailboxPlus,
		Email: "user+legacy@gmail.com", CreatedAt: createdAt, ReleasedAt: createdAt.Add(time.Minute),
	}
	orderNo := historicalGmailAllocationOrderNo(cmd)
	repo := &historicalGmailReplayRepo{existing: domain.UnifiedAllocation{
		Type: domain.AllocationTypeGmail, ID: 40, OrderNo: orderNo, ProjectID: cmd.ProjectID,
		ProductID: 20, ResourceID: cmd.ResourceID, Mailbox: string(cmd.Mailbox), Email: cmd.Email,
		Status: domain.AllocationStatusReleased,
	}}

	result, err := NewUseCase(repo).ImportHistoricalGmailAllocation(context.Background(), cmd)

	if err != nil {
		t.Fatalf("ImportHistoricalGmailAllocation() error = %v", err)
	}
	if result == nil || result.ProductID != 20 || repo.existing.ProductID != 20 {
		t.Fatalf("historical allocation product changed: result=%#v stored=%#v", result, repo.existing)
	}
	if result.Created {
		t.Fatal("replayed historical allocation reported as newly created")
	}

	cmd.Mailbox = domain.GmailMailboxDot
	cmd.Email = "u.ser@gmail.com"
	repo.existing.OrderNo = historicalGmailAllocationOrderNo(cmd)
	repo.existing.Mailbox = string(cmd.Mailbox)
	repo.existing.Email = cmd.Email
	result, err = NewUseCase(repo).ImportHistoricalGmailAllocation(context.Background(), cmd)
	if err != nil || result == nil || result.ProductID != 20 {
		t.Fatalf("dot allocation replay = %#v, %v; want preserved pre-special product", result, err)
	}
}

func TestAllocationRuntimeSettingsApplyToNewWork(t *testing.T) {
	settings := map[string]string{
		"candidate_window_size":              "2",
		"global_candidate_window":            "3",
		"bucket_probe_count":                 "2",
		"alias_generation_window":            "3",
		"candidate_retry_count":              "2",
		"dot_alias_capacity_per_resource":    "2",
		"inventory_refresh_interval_minutes": "4",
		"inventory_cache_hard_ttl_hours":     "6",
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
	if InventoryRefreshIntervalValue() != 4*time.Minute || inventoryCacheHardTTLValue() != 6*time.Hour {
		t.Fatal("inventory runtime settings were not applied")
	}
}

func TestAllocationRuntimeSettingsClampUnsafeValues(t *testing.T) {
	settings := map[string]string{
		"candidate_window_size":              "2147483647",
		"global_candidate_window":            "2147483647",
		"bucket_probe_count":                 "2147483647",
		"alias_generation_window":            "2147483647",
		"candidate_retry_count":              "2147483647",
		"dot_alias_capacity_per_resource":    "2147483647",
		"inventory_refresh_interval_minutes": "1000000",
		"inventory_cache_hard_ttl_hours":     "100000",
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
	if InventoryRefreshIntervalValue() != maxInventoryRefreshInterval || inventoryCacheHardTTLValue() != maxInventoryCacheHardTTL {
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

func TestSpecifiedDomainTLDReusesMailboxThroughResourceBucket(t *testing.T) {
	repo := &generatedMailboxRetryRepo{
		candidate: DomainCandidate{ResourceID: 1, OwnerUserID: 2, Domain: "example.com", MailboxDailyLimit: 10},
		reusable:  &GeneratedMailboxCandidate{ID: 7, ResourceID: 1, Email: "existing@example.com"},
	}
	result, err := NewUseCase(repo).allocateDomainOnce(
		context.Background(),
		AllocateCommand{OrderNo: "order-1", BuyerUserID: 3, SupplyScope: domain.SupplyScopeOwned, EmailSuffix: "com", ensureOrderGuard: func(context.Context, domain.AllocationType) error { return nil }},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
	)
	if err != nil || result == nil || result.Email != "existing@example.com" {
		t.Fatalf("specified-TLD reuse result = %#v, error = %v", result, err)
	}
	if repo.generatedLists != 0 || len(repo.domainBuckets) != 1 || repo.domainBuckets[0] < 0 || repo.calls != 0 {
		t.Fatalf("generated lists/domain buckets/generated calls = %d/%v/%d, want 0/[bucket]/0", repo.generatedLists, repo.domainBuckets, repo.calls)
	}
}

func TestDomainAllocationRejectsWrongDeliveryTLDBeforeUsage(t *testing.T) {
	repo := &generatedMailboxRetryRepo{}
	result, err := NewUseCase(repo).createDomainAllocation(
		context.Background(),
		AllocateCommand{EmailSuffix: "com", ensureOrderGuard: func(context.Context, domain.AllocationType) error { return nil }},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
		1,
		7,
		"wrong@other.cn",
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

func TestDomainProductRejectsPrivateMailboxWithoutOwnedScope(t *testing.T) {
	repo := &allocationLockRepo{config: ProductAllocationConfig{
		ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeDomain,
	}}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "order-1", BuyerUserID: 3, ProjectProductID: 5,
		SupplyScope: domain.SupplyScopePublic, EmailSuffix: "alice@example.com",
	})
	if !errors.Is(err, domain.ErrInvalidAllocationRequest) || result != nil {
		t.Fatalf("Allocate() result = %#v, error = %v; want invalid private mailbox", result, err)
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

func TestDomainAllocationGeneratesAnotherAddressAfterProjectHistory(t *testing.T) {
	repo := &generatedMailboxRetryRepo{
		candidate:       DomainCandidate{ResourceID: 1, OwnerUserID: 2, Domain: "example.com", MailboxDailyLimit: 10},
		historicalFirst: true,
	}
	result, err := NewUseCase(repo).tryDomainCandidate(
		context.Background(),
		AllocateCommand{OrderNo: "order-1", BuyerUserID: 3, SupplyScope: domain.SupplyScopeOwned, ensureOrderGuard: func(context.Context, domain.AllocationType) error { return nil }},
		ProductAllocationConfig{ProjectID: 4, ProductID: 5},
		repo.candidate,
		time.Now().UTC(),
	)
	if err != nil || result == nil {
		t.Fatalf("tryDomainCandidate() result = %#v, error = %v", result, err)
	}
	if repo.calls != 3 || repo.historyChecks != 2 || len(repo.historyEmails) != 2 || !strings.HasSuffix(repo.historyEmails[0], "@example.com") {
		t.Fatalf("generated attempts/history checks/result = %d/%d/%#v; want 3/2/new mailbox", repo.calls, repo.historyChecks, result)
	}
}
