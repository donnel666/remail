package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func daumSuffixRepo() *daumAllocationLockRepo {
	return &daumAllocationLockRepo{
		allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeDaum,
			CodeEnabled: true, PurchaseEnabled: true, CodeSupplierPrice: "0.25", PurchaseSupplierPrice: "0.50",
		}},
		daumCandidates: []DaumCandidate{{ResourceID: 9, Mailbox: "main", Email: "one@daum.net"}, {ResourceID: 9, Mailbox: "alias", ExplicitAliasID: func() *uint { id := uint(90); return &id }(), Email: "one@hanmail.net"}},
	}
}

func TestDaumAllocationPinsExactSuffixThroughCandidateAndLock(t *testing.T) {
	for _, suffix := range []string{"", "daum.net", "hanmail.net", "daum.me", "other.example"} {
		t.Run(suffix, func(t *testing.T) {
			repo := daumSuffixRepo()
			uc := NewUseCase(repo)
			uc.SetDaumProtocolReady(true)
			result, err := uc.Allocate(context.Background(), AllocateCommand{
				OrderNo: "daum-suffix", BuyerUserID: 3, ProjectProductID: 5,
				ServiceMode: domain.ServiceModePurchase, EmailSuffix: suffix,
			})
			if suffix != "" && !coredomain.IsDaumEmailSuffix(suffix) {
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

type daumScopedSuffixRepo struct {
	*daumAllocationLockRepo
	inventory map[domain.SupplyScope]map[string]int64
	scopes    []domain.SupplyScope
	queryErr  error
}

func (r *daumScopedSuffixRepo) ListProductSuffixInventory(_ context.Context, _ ProductAllocationConfig, _ uint, scope domain.SupplyScope) (map[string]int64, error) {
	r.scopes = append(r.scopes, scope)
	return r.inventory[scope], r.queryErr
}

func (r *daumScopedSuffixRepo) ListDaumSourceCandidates(ctx context.Context, projectID, buyerID uint, scope domain.SupplyScope, buckets []uint16, limit int, suffix string) ([]DaumCandidate, error) {
	if r.inventory[scope][suffix] <= 0 {
		r.listedSuffixes = append(r.listedSuffixes, suffix)
		return nil, nil
	}
	return r.daumAllocationLockRepo.ListDaumSourceCandidates(ctx, projectID, buyerID, scope, buckets, limit, suffix)
}

func TestDaumRandomSuffixPreservesOwnedPriorityAndPinsAllocation(t *testing.T) {
	for _, owned := range []bool{true, false} {
		repo := &daumScopedSuffixRepo{daumAllocationLockRepo: daumSuffixRepo(), inventory: map[domain.SupplyScope]map[string]int64{
			domain.SupplyScopePublic: {"daum.net": 1000, "daum.me": 10000},
		}}
		if owned {
			repo.inventory[domain.SupplyScopeOwned] = map[string]int64{"hanmail.net": 1}
		}
		uc := NewUseCase(repo)
		uc.SetDaumProtocolReady(true)
		result, err := uc.Allocate(context.Background(), AllocateCommand{
			OrderNo: "daum-random", BuyerUserID: 3, ProjectProductID: 5,
			EmailSuffix: coredomain.RandomDaumSuffixSelector, ServiceMode: domain.ServiceModeCode,
			SupplyScopes: []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic},
		})
		require.NoError(t, err)
		want, scopes := "daum.net", []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic}
		wantScope := domain.SupplyScopePublic
		if owned {
			want, scopes = "hanmail.net", scopes[:1]
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

func TestDaumRandomSuffixWeightsAndProtocolGate(t *testing.T) {
	for ticket, want := range []string{"daum.net", "daum.net", "hanmail.net"} {
		suffix, ok := chooseWeightedInventorySuffix(map[string]int64{"daum.net": 2, "hanmail.net": 1, "daum.me": 1000}, func(suffix string) bool {
			return randomSuffixMatchesProduct(coredomain.RandomDaumSuffixSelector, coredomain.ProductTypeDaum, suffix)
		}, func(total int64) int64 { require.EqualValues(t, 3, total); return int64(ticket) })
		require.True(t, ok)
		require.Equal(t, want, suffix)
	}
	repo := &daumScopedSuffixRepo{daumAllocationLockRepo: daumSuffixRepo()}
	uc := NewUseCase(repo)
	request := ProductSuffixSelectionRequest{ProductID: 5, ProjectID: 4, BuyerUserID: 3, Selector: coredomain.RandomDaumSuffixSelector}
	_, err := uc.SelectRandomInventorySuffix(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	require.Empty(t, repo.scopes)
	uc.SetDaumProtocolReady(true)
	repo.config.ProductType = coredomain.ProductTypeMicrosoft
	_, err = uc.SelectRandomInventorySuffix(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
}
