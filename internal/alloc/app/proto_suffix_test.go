package app

import (
	"context"
	"errors"
	"testing"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func protoSuffixRepo() *protoAllocationLockRepo {
	return &protoAllocationLockRepo{
		allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeProto,
			CodeEnabled: true, PurchaseEnabled: true, CodeSupplierPrice: "0.25", PurchaseSupplierPrice: "0.50",
		}},
		protoCandidates: []ProtoCandidate{{ResourceID: 9, Email: "one@proton.me"}, {ResourceID: 10, Email: "one@protonmail.com"}},
	}
}

func TestProtoAllocationPinsExactSuffixThroughCandidateAndLock(t *testing.T) {
	for _, suffix := range []string{"", "proton.me", "protonmail.com", "proto.me", "other.example"} {
		t.Run(suffix, func(t *testing.T) {
			repo := protoSuffixRepo()
			uc := NewUseCase(repo)
			uc.SetProtoProtocolReady(true)
			result, err := uc.Allocate(context.Background(), AllocateCommand{
				OrderNo: "proto-suffix", BuyerUserID: 3, ProjectProductID: 5,
				ServiceMode: domain.ServiceModePurchase, EmailSuffix: suffix,
			})
			if suffix != "" && !coredomain.IsProtoEmailSuffix(suffix) {
				require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
				require.Nil(t, result)
				require.Empty(t, repo.listedSuffixes)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{suffix}, repo.listedSuffixes)
			require.Equal(t, []string{suffix}, repo.lockedSuffixes)
			if suffix != "" {
				require.Equal(t, "one@"+suffix, result.Email)
			}
			require.Equal(t, "0.50", repo.created.CostPointsSnapshot)
		})
	}
}

type protoScopedSuffixRepo struct {
	*protoAllocationLockRepo
	inventory map[domain.SupplyScope]map[string]int64
	scopes    []domain.SupplyScope
	queryErr  error
}

func (r *protoScopedSuffixRepo) ListProductSuffixInventory(_ context.Context, _ ProductAllocationConfig, _ uint, scope domain.SupplyScope) (map[string]int64, error) {
	r.scopes = append(r.scopes, scope)
	return r.inventory[scope], r.queryErr
}

func (r *protoScopedSuffixRepo) ListProtoSourceCandidates(ctx context.Context, projectID, buyerID uint, scope domain.SupplyScope, bucket *uint16, limit int, suffix string) ([]ProtoCandidate, error) {
	if r.inventory[scope][suffix] <= 0 {
		r.listedSuffixes = append(r.listedSuffixes, suffix)
		return nil, nil
	}
	return r.protoAllocationLockRepo.ListProtoSourceCandidates(ctx, projectID, buyerID, scope, bucket, limit, suffix)
}

func TestProtoRandomSuffixPreservesOwnedPriorityAndPinsAllocation(t *testing.T) {
	for _, owned := range []bool{true, false} {
		repo := &protoScopedSuffixRepo{protoAllocationLockRepo: protoSuffixRepo(), inventory: map[domain.SupplyScope]map[string]int64{
			domain.SupplyScopePublic: {"proton.me": 1000, "proto.me": 10000},
		}}
		if owned {
			repo.inventory[domain.SupplyScopeOwned] = map[string]int64{"protonmail.com": 1}
		}
		uc := NewUseCase(repo)
		uc.SetProtoProtocolReady(true)
		result, err := uc.Allocate(context.Background(), AllocateCommand{
			OrderNo: "proto-random", BuyerUserID: 3, ProjectProductID: 5,
			EmailSuffix: coredomain.RandomProtoSuffixSelector, ServiceMode: domain.ServiceModeCode,
			SupplyScopes: []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic},
		})
		require.NoError(t, err)
		want, scopes := "proton.me", []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic}
		wantScope := domain.SupplyScopePublic
		if owned {
			want, scopes = "protonmail.com", scopes[:1]
			wantScope = domain.SupplyScopeOwned
		}
		require.Equal(t, "one@"+want, result.Email)
		require.Equal(t, wantScope, result.SupplyScope)
		require.Equal(t, scopes, repo.scopes)
		for _, suffix := range repo.listedSuffixes {
			require.Equal(t, want, suffix)
		}
		require.Equal(t, []string{want}, repo.lockedSuffixes)
	}
}

func TestProtoRandomSuffixWeightsAndProtocolGate(t *testing.T) {
	for ticket, want := range []string{"proton.me", "proton.me", "protonmail.com"} {
		suffix, ok := chooseWeightedInventorySuffix(map[string]int64{"proton.me": 2, "protonmail.com": 1, "proto.me": 1000}, func(suffix string) bool {
			return randomSuffixMatchesProduct(coredomain.RandomProtoSuffixSelector, coredomain.ProductTypeProto, suffix)
		}, func(total int64) int64 { require.EqualValues(t, 3, total); return int64(ticket) })
		require.True(t, ok)
		require.Equal(t, want, suffix)
	}
	repo := &protoScopedSuffixRepo{protoAllocationLockRepo: protoSuffixRepo()}
	uc := NewUseCase(repo)
	request := ProductSuffixSelectionRequest{ProductID: 5, ProjectID: 4, BuyerUserID: 3, Selector: coredomain.RandomProtoSuffixSelector}
	_, err := uc.SelectRandomInventorySuffix(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	require.Empty(t, repo.scopes)
	uc.SetProtoProtocolReady(true)
	repo.config.ProductType = coredomain.ProductTypeMicrosoft
	_, err = uc.SelectRandomInventorySuffix(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
}

type protoSuffixCache struct {
	*warmOnInitializeInventoryCache
	advanced  []InventoryCacheEntry
	byProject map[uint]*ProjectProductInventoryTotals
}

func (c *protoSuffixCache) GetProductInventorySnapshots(ctx context.Context, ids []uint) (map[uint]*ProjectProductInventoryTotals, error) {
	if c.byProject == nil {
		return c.warmOnInitializeInventoryCache.GetProductInventorySnapshots(ctx, ids)
	}
	result := make(map[uint]*ProjectProductInventoryTotals, len(ids))
	for _, id := range ids {
		if snapshot := c.byProject[id]; snapshot != nil {
			result[id] = snapshot
		}
	}
	return result, nil
}

func (c *protoSuffixCache) GetProductInventoryTotals(context.Context, uint) (*ProjectProductInventoryTotals, error) {
	return c.totals, nil
}
func (*protoSuffixCache) IsProductUnavailable(context.Context, ProductInventoryAvailabilityRequest) (bool, error) {
	return false, nil
}
func (c *protoSuffixCache) AdvanceInventory(_ context.Context, entries []InventoryCacheEntry) error {
	c.advanced = append(c.advanced, entries...)
	return nil
}

type protoSuffixRefreshQueue struct{ calls int }

func (q *protoSuffixRefreshQueue) EnqueueInventoryRefresh(context.Context) error {
	q.calls++
	return nil
}
func (*protoSuffixRefreshQueue) EnqueueInventoryRefreshContinuation(context.Context) error {
	return nil
}

func TestProtoLegacyAggregateSuffixCacheFallsBackAndSchedulesRefresh(t *testing.T) {
	for _, suffix := range []string{coredomain.RandomProtoSuffixSelector, "proton.me", "protonmail.com"} {
		t.Run(suffix, func(t *testing.T) {
			repo := &protoScopedSuffixRepo{protoAllocationLockRepo: protoSuffixRepo(), inventory: map[domain.SupplyScope]map[string]int64{domain.SupplyScopePublic: {"protonmail.com": 4}}}
			cache := &protoSuffixCache{warmOnInitializeInventoryCache: &warmOnInitializeInventoryCache{initialized: true, totals: &ProjectProductInventoryTotals{ProjectID: 4, TotalAvailable: 4, Items: []ProductInventoryTotal{{
				ProductID: 5, ProductType: coredomain.ProductTypeProto, TotalAvailable: 4, PublicAvailable: 4,
			}}}}}
			queue := &protoSuffixRefreshQueue{}
			uc := NewUseCase(repo, queue)
			uc.SetInventoryCache(cache)
			uc.SetProtoProtocolReady(true)
			available, err := uc.HasProductInventory(context.Background(), ProductInventoryAvailabilityRequest{ProjectID: 4, ProductID: 5, EmailSuffix: suffix, PublicOnly: true})
			require.NoError(t, err)
			require.True(t, available, "old aggregate is unknown, not a known-empty suffix")
			require.Empty(t, repo.scopes, "precheck must leave reservation to the allocator")
			require.Len(t, cache.advanced, 1)
			require.Equal(t, 1, queue.calls)
			selected, err := uc.SelectRandomInventorySuffix(context.Background(), ProductSuffixSelectionRequest{ProjectID: 4, ProductID: 5, BuyerUserID: 3, Selector: coredomain.RandomProtoSuffixSelector})
			require.NoError(t, err)
			require.Equal(t, "protonmail.com", selected)
			require.Equal(t, []domain.SupplyScope{domain.SupplyScopePublic}, repo.scopes)
			require.Len(t, cache.advanced, 2)
			require.Equal(t, 2, queue.calls)
			require.Empty(t, cache.totals.Items[0].Suffixes, "fallback must not rewrite the shared snapshot")
			repo.queryErr = errors.New("scoped suffix query unavailable")
			_, err = uc.SelectRandomInventorySuffix(context.Background(), ProductSuffixSelectionRequest{ProjectID: 4, ProductID: 5, BuyerUserID: 3, Selector: coredomain.RandomProtoSuffixSelector})
			require.ErrorIs(t, err, repo.queryErr)
		})
	}
}

func TestProtoSuffixCacheKnownCountsAndOtherProvidersRemainUnchanged(t *testing.T) {
	for _, typ := range []coredomain.ProductType{coredomain.ProductTypeProto, coredomain.ProductTypeMicrosoft, coredomain.ProductTypeDomain, coredomain.ProductTypeGmail} {
		totals := &ProjectProductInventoryTotals{Items: []ProductInventoryTotal{{ProductID: 5, ProductType: typ, TotalAvailable: 4, PublicAvailable: 4}}}
		_, known := productInventoryAvailable(totals, ProductInventoryAvailabilityRequest{ProductID: 5, EmailSuffix: "proton.me", PublicOnly: true})
		require.Equal(t, typ != coredomain.ProductTypeProto && typ != coredomain.ProductTypeMicrosoft, known)
		if typ == coredomain.ProductTypeProto {
			totals.Items[0].Suffixes = []ProductInventorySuffixTotal{{Suffix: "proton.me", TotalAvailable: 4, PublicAvailable: 4}}
			available, known := productInventoryAvailable(totals, ProductInventoryAvailabilityRequest{ProductID: 5, EmailSuffix: coredomain.RandomProtoSuffixSelector, PublicOnly: true})
			require.True(t, available && known)
			available, known = productInventoryAvailable(totals, ProductInventoryAvailabilityRequest{ProductID: 5, EmailSuffix: "protonmail.com", PublicOnly: true})
			require.False(t, available)
			require.True(t, known)
			uc := NewUseCase(nil)
			hidden := uc.protoInventoryTotals(totals)
			require.Zero(t, hidden.Items[0].Suffixes[0].TotalAvailable)
			require.Zero(t, hidden.Items[0].Suffixes[0].PublicAvailable)
			require.EqualValues(t, 4, totals.Items[0].Suffixes[0].PublicAvailable)
		}
	}
}

func TestProtoColdSuffixSelectionIsLiveWhileOtherProvidersStayCold(t *testing.T) {
	repo := &protoScopedSuffixRepo{protoAllocationLockRepo: protoSuffixRepo(), inventory: map[domain.SupplyScope]map[string]int64{domain.SupplyScopePublic: {"proton.me": 4}}}
	cache := &protoSuffixCache{warmOnInitializeInventoryCache: &warmOnInitializeInventoryCache{initialized: true, totals: &ProjectProductInventoryTotals{ProjectID: 4, Cold: true}}}
	queue := &protoSuffixRefreshQueue{}
	uc := NewUseCase(repo, queue)
	uc.SetInventoryCache(cache)
	uc.SetProtoProtocolReady(true)
	request := ProductSuffixSelectionRequest{ProjectID: 4, ProductID: 5, BuyerUserID: 3, Selector: coredomain.RandomProtoSuffixSelector}
	suffix, err := uc.SelectRandomInventorySuffix(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "proton.me", suffix)
	require.Equal(t, []domain.SupplyScope{domain.SupplyScopePublic}, repo.scopes)
	for _, selector := range []string{coredomain.RandomProtoSuffixSelector, "proton.me", "protonmail.com"} {
		available, err := uc.HasProductInventory(context.Background(), ProductInventoryAvailabilityRequest{ProjectID: 4, ProductID: 5, EmailSuffix: selector, PublicOnly: true})
		require.NoError(t, err)
		require.True(t, available)
	}
	require.Equal(t, 4, queue.calls)
	require.Len(t, cache.advanced, 4)
	uc.SetProtoProtocolReady(false)
	_, err = uc.SelectRandomInventorySuffix(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	available, err := uc.HasProductInventory(context.Background(), ProductInventoryAvailabilityRequest{ProjectID: 4, ProductID: 5, EmailSuffix: "proton.me", PublicOnly: true})
	require.NoError(t, err)
	require.False(t, available)
	require.Len(t, repo.scopes, 1)
	uc.SetProtoProtocolReady(true)
	repo.config.ProductType = coredomain.ProductTypeMicrosoft
	request.Selector = coredomain.RandomMicrosoftSuffixSelector
	_, err = uc.SelectRandomInventorySuffix(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	available, err = uc.HasProductInventory(context.Background(), ProductInventoryAvailabilityRequest{ProjectID: 4, ProductID: 5, EmailSuffix: "outlook.com", PublicOnly: true})
	require.NoError(t, err)
	require.False(t, available)
	require.Len(t, repo.scopes, 1)
	require.Equal(t, 4, queue.calls)
}

func TestProtoWarmSuffixSnapshotAvoidsLiveQueries(t *testing.T) {
	repo := &protoScopedSuffixRepo{protoAllocationLockRepo: protoSuffixRepo(), queryErr: errors.New("must not read the database")}
	cache := &warmOnInitializeInventoryCache{initialized: true, totals: &ProjectProductInventoryTotals{ProjectID: 4, Items: []ProductInventoryTotal{{
		ProductID: 5, ProductType: coredomain.ProductTypeProto, TotalAvailable: 3, PublicAvailable: 3,
		Suffixes: []ProductInventorySuffixTotal{{Suffix: "protonmail.com", TotalAvailable: 3, PublicAvailable: 3}},
	}}}}
	uc := NewUseCase(repo)
	uc.SetInventoryCache(cache)
	uc.SetProtoProtocolReady(true)
	suffix, err := uc.SelectRandomInventorySuffix(context.Background(), ProductSuffixSelectionRequest{ProjectID: 4, ProductID: 5, BuyerUserID: 3, Selector: coredomain.RandomProtoSuffixSelector})
	require.NoError(t, err)
	require.Equal(t, "protonmail.com", suffix)
	require.Empty(t, repo.scopes)
}

func TestProtoDisabledProtocolNeverFailsOpenForUnknownSuffixInventory(t *testing.T) {
	for _, totals := range []*ProjectProductInventoryTotals{nil, {ProjectID: 4, Cold: true}, {ProjectID: 4}} {
		uc := NewUseCase(nil)
		if totals != nil {
			uc.SetInventoryCache(&protoSuffixCache{warmOnInitializeInventoryCache: &warmOnInitializeInventoryCache{initialized: true, totals: totals}})
		}
		for _, suffix := range []string{coredomain.RandomProtoSuffixSelector, "proton.me", "protonmail.com"} {
			available, err := uc.HasProductInventory(context.Background(), ProductInventoryAvailabilityRequest{ProjectID: 4, ProductID: 5, EmailSuffix: suffix, PublicOnly: true})
			require.NoError(t, err)
			require.False(t, available)
		}
	}
}

func TestProtoSnapshotReadsQueueMissingSuffixDetailWithoutChangingInventory(t *testing.T) {
	legacy := &ProjectProductInventoryTotals{ProjectID: 4, TotalAvailable: 7, Items: []ProductInventoryTotal{{
		ProductID: 5, ProductType: coredomain.ProductTypeProto, TotalAvailable: 7, PublicAvailable: 4,
		Suffixes: []ProductInventorySuffixTotal{{Suffix: "proton.me", TotalAvailable: 3}},
	}}}
	current := &ProjectProductInventoryTotals{ProjectID: 8, TotalAvailable: 2, Items: []ProductInventoryTotal{{
		ProductID: 9, ProductType: coredomain.ProductTypeProto, TotalAvailable: 2, PublicAvailable: 2,
		Suffixes: []ProductInventorySuffixTotal{{Suffix: "protonmail.com", TotalAvailable: 2, PublicAvailable: 2}},
	}}}
	microsoft := &ProjectProductInventoryTotals{ProjectID: 12, TotalAvailable: 4, Items: []ProductInventoryTotal{{
		ProductID: 13, ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: 4, PublicAvailable: 4,
	}}}
	cache := &protoSuffixCache{byProject: map[uint]*ProjectProductInventoryTotals{4: legacy, 8: current, 12: microsoft}}
	queue := &protoSuffixRefreshQueue{}
	uc := NewUseCase(nil, queue)
	uc.SetInventoryCache(cache)
	uc.SetProtoProtocolReady(true)
	snapshots, err := uc.GetProductInventorySnapshots(context.Background(), []uint{4, 8, 12})
	require.NoError(t, err)
	require.Same(t, legacy, snapshots[4])
	require.Same(t, current, snapshots[8])
	require.Same(t, microsoft, snapshots[12])
	require.Equal(t, []InventoryCacheEntry{{Kind: InventoryCacheProducts, ProjectID: 4}}, cache.advanced)
	require.Equal(t, 1, queue.calls)
	require.Zero(t, snapshots[4].Items[0].Suffixes[0].PublicAvailable, "reads must not invent public child stock")
	uc.SetProtoProtocolReady(false)
	cache.advanced, queue.calls = nil, 0
	snapshots, err = uc.GetProductInventorySnapshots(context.Background(), []uint{4, 8, 12})
	require.NoError(t, err)
	require.Empty(t, cache.advanced)
	require.Zero(t, queue.calls)
	require.Zero(t, snapshots[4].TotalAvailable)
	require.Zero(t, snapshots[8].Items[0].Suffixes[0].PublicAvailable)
	require.Same(t, microsoft, snapshots[12])
	require.EqualValues(t, 7, legacy.TotalAvailable)
}

func TestProtoPrivateSuffixMergeKeepsModeTotalsAndPublicCounts(t *testing.T) {
	code, purchase := int64(2), int64(2)
	source := &ProjectProductInventoryTotals{TotalAvailable: 2, Items: []ProductInventoryTotal{{
		ProductID: 5, ProductType: coredomain.ProductTypeProto, TotalAvailable: 2, PublicAvailable: 2,
		CodeAvailable: &code, PurchaseAvailable: &purchase, CodePublicAvailable: &code, PurchasePublicAvailable: &purchase,
		Suffixes: []ProductInventorySuffixTotal{{Suffix: "proton.me", TotalAvailable: 2, PublicAvailable: 2}},
	}}}
	for _, snapshot := range []*ProjectProductInventoryTotals{source, {}} {
		merged := cloneProductInventoryTotals(snapshot)
		mergePrivateProductInventory(merged, []PrivateProductInventoryTotal{{ProductID: 5, Suffix: "proton.me", Available: 3}, {ProductID: 5, Suffix: "protonmail.com", Available: 4}}, coredomain.ProductTypeProto)
		require.Equal(t, snapshot.TotalAvailable+7, merged.TotalAvailable)
		require.Len(t, merged.Items[0].Suffixes, 2)
		require.Equal(t, snapshot.TotalAvailable+7, merged.Items[0].TotalAvailable)
		if len(snapshot.Items) > 0 {
			require.EqualValues(t, 9, *merged.Items[0].CodeAvailable)
			require.EqualValues(t, 9, *merged.Items[0].PurchaseAvailable)
			require.EqualValues(t, 2, *merged.Items[0].CodePublicAvailable)
			require.EqualValues(t, 2, *merged.Items[0].PurchasePublicAvailable)
		} else {
			// A private-only item uses the existing mode fallback to TotalAvailable.
			require.Nil(t, merged.Items[0].CodeAvailable)
			require.Nil(t, merged.Items[0].PurchaseAvailable)
		}
	}
	require.EqualValues(t, 2, *source.Items[0].CodeAvailable)
	require.EqualValues(t, 2, source.Items[0].Suffixes[0].TotalAvailable)
	legacy := cloneProductInventoryTotals(source)
	legacy.Items[0].Suffixes = nil
	mergePrivateProductInventory(legacy, []PrivateProductInventoryTotal{{ProductID: 5, Suffix: "proton.me", Available: 3}}, coredomain.ProductTypeProto)
	require.True(t, protoSuffixInventoryMissing(legacy.Items[0]))
	available, known := productInventoryAvailable(legacy, ProductInventoryAvailabilityRequest{ProductID: 5, EmailSuffix: "protonmail.com", PublicOnly: true})
	require.False(t, available)
	require.False(t, known, "private suffix detail cannot prove a legacy public suffix is empty")
	uc := NewUseCase(nil)
	hidden := uc.protoInventoryTotals(legacy)
	available, known = productInventoryAvailable(hidden, ProductInventoryAvailabilityRequest{ProductID: 5, EmailSuffix: "protonmail.com", PublicOnly: true})
	require.False(t, available)
	require.True(t, known, "disabled protocol must stay known-zero, not fail open")
}
