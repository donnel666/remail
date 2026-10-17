package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type daumProbeRepo struct {
	*daumAllocationLockRepo
	batches          [][]uint16
	state            string
	retryTransaction bool
}

func (r *daumProbeRepo) ListDaumSourceCandidates(_ context.Context, _, _ uint, _ domain.SupplyScope, buckets []uint16, _ int, _ string) ([]DaumCandidate, error) {
	r.batches = append(r.batches, append([]uint16(nil), buckets...))
	if len(r.batches) == 1 && r.state != "empty" {
		return r.daumCandidates, nil
	}
	return nil, nil
}
func (r *daumProbeRepo) LockResourceRoot(context.Context, uint, domain.AllocationType) (bool, error) {
	return r.state != "busy", nil
}
func (r *daumProbeRepo) CreateDaumAllocation(ctx context.Context, row *domain.DaumAllocation) error {
	if r.state == "write conflict" {
		return domain.ErrAllocationConflict
	}
	return r.daumAllocationLockRepo.CreateDaumAllocation(ctx, row)
}
func (r *daumProbeRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	err := r.allocationLockRepo.WithTx(ctx, fn)
	if err != nil && r.retryTransaction {
		return r.allocationLockRepo.WithTx(ctx, fn)
	}
	return err
}

func TestDaumBucketBudgetSurvivesTransactionRetriesWithoutGlobalScan(t *testing.T) {
	for _, state := range []string{"empty", "busy", "write conflict"} {
		t.Run(state, func(t *testing.T) {
			repo := &daumProbeRepo{daumAllocationLockRepo: daumSuffixRepo(), state: state, retryTransaction: true}
			uc := NewUseCase(repo)
			uc.SetDaumProtocolReady(true)
			result, err := uc.Allocate(context.Background(), AllocateCommand{OrderNo: "daum-budget", BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: "daum.net", ServiceMode: domain.ServiceModeCode})
			require.Nil(t, result)
			require.Error(t, err)
			require.False(t, errors.Is(err, domain.ErrDefinitiveInventoryExhausted))
			require.Len(t, repo.batches, bucketProbeCountValue()+(int(DaumBucketCount)-bucketProbeCountValue()+MicrosoftExpansionBuckets-1)/MicrosoftExpansionBuckets)
			start := bucketProbeSequence("daum-budget", 4, "main", DaumBucketCount)[0]
			seen := map[uint16]bool{}
			index := 0
			for i, batch := range repo.batches {
				if i < bucketProbeCountValue() {
					require.Len(t, batch, 1)
				} else {
					require.Len(t, batch, min(MicrosoftExpansionBuckets, int(DaumBucketCount)-index))
				}
				for _, bucket := range batch {
					require.False(t, seen[bucket], "retries must not reset the query window")
					require.Equal(t, (start+uint16(index))%DaumBucketCount, bucket)
					seen[bucket] = true
					index++
				}
			}
			require.Len(t, seen, int(DaumBucketCount))
		})
	}
}

func TestDaumBucketProbeWrapsFrom255ToZero(t *testing.T) {
	var orderNo string
	for nonce := 0; ; nonce++ {
		orderNo = fmt.Sprintf("daum-wrap-%d", nonce)
		if bucketProbeSequence(orderNo, 4, "main", DaumBucketCount)[0] == 255 {
			break
		}
	}
	repo := &daumProbeRepo{daumAllocationLockRepo: daumSuffixRepo(), state: "empty", retryTransaction: true}
	uc := NewUseCase(repo)
	uc.SetDaumProtocolReady(true)
	_, err := uc.Allocate(context.Background(), AllocateCommand{OrderNo: orderNo, BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: "daum.net", ServiceMode: domain.ServiceModeCode})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	require.Equal(t, []uint16{255}, repo.batches[0])
	require.Equal(t, []uint16{0}, repo.batches[1])
	seen := map[uint16]bool{}
	for _, batch := range repo.batches {
		for _, bucket := range batch {
			require.Less(t, bucket, uint16(256))
			require.False(t, seen[bucket], "retry must not repeat a bucket")
			seen[bucket] = true
		}
	}
	require.Len(t, seen, int(DaumBucketCount))
}

func TestDaumFullBucketCoverageKeepsConfiguredQueryBudget(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()["bucket_probe_count"]
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set("bucket_probe_count", previous)
		} else {
			runtimeconfig.Delete("bucket_probe_count")
		}
	})
	for _, probes := range []int{1, 64} {
		runtimeconfig.Set("bucket_probe_count", fmt.Sprint(probes))
		repo := &daumProbeRepo{daumAllocationLockRepo: daumSuffixRepo(), state: "empty", retryTransaction: true}
		uc := NewUseCase(repo)
		uc.SetDaumProtocolReady(true)
		_, err := uc.Allocate(context.Background(), AllocateCommand{OrderNo: "configured-daum", BuyerUserID: 3, ProjectProductID: 5, EmailSuffix: "daum.net", ServiceMode: domain.ServiceModeCode})
		require.ErrorIs(t, err, domain.ErrInsufficientInventory)
		seen := map[uint16]bool{}
		for _, batch := range repo.batches {
			require.NotEmpty(t, batch)
			require.LessOrEqual(t, len(batch), 100)
			for _, bucket := range batch {
				require.False(t, seen[bucket])
				seen[bucket] = true
			}
		}
		require.Len(t, seen, 256)
		require.Len(t, repo.batches, probes+(256-probes+99)/100)
		require.LessOrEqual(t, len(repo.batches), 66)
	}
}

func TestDaumAcceptedOrderSurvivesModeDisableLikeMicrosoft(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		repo := daumSuffixRepo()
		repo.config.PurchaseEnabled = false
		uc := NewUseCase(repo)
		uc.SetDaumProtocolReady(true)
		result, err := uc.Allocate(context.Background(), AllocateCommand{OrderNo: "accepted-daum", BuyerUserID: 3, ProjectProductID: 5, ServiceMode: domain.ServiceModePurchase, FulfillExistingOrder: accepted})
		if accepted {
			require.NoError(t, err)
			require.NotNil(t, result)
		} else {
			require.ErrorIs(t, err, domain.ErrProjectNotAllocatable)
			require.Nil(t, result)
		}
	}
}

func TestDaumColdCacheAndMissingSuffixMatchMicrosoft(t *testing.T) {
	for _, provider := range []coredomain.ProductType{coredomain.ProductTypeMicrosoft, coredomain.ProductTypeDaum} {
		repo := &daumScopedSuffixRepo{daumAllocationLockRepo: daumSuffixRepo(), inventory: map[domain.SupplyScope]map[string]int64{domain.SupplyScopePublic: {"daum.net": 1, "outlook.com": 1}}}
		repo.config.ProductType = provider
		uc := NewUseCase(repo)
		uc.SetDaumProtocolReady(true)
		uc.SetInventoryCache(&protoSuffixCache{warmOnInitializeInventoryCache: &warmOnInitializeInventoryCache{initialized: true, totals: &ProjectProductInventoryTotals{ProjectID: 4, Cold: true}}})
		selector, suffix := coredomain.RandomDaumSuffixSelector, "daum.net"
		if provider == coredomain.ProductTypeMicrosoft {
			selector, suffix = coredomain.RandomMicrosoftSuffixSelector, "outlook.com"
		}
		_, err := uc.SelectRandomInventorySuffix(context.Background(), ProductSuffixSelectionRequest{ProductID: 5, ProjectID: 4, BuyerUserID: 3, Selector: selector})
		require.ErrorIs(t, err, domain.ErrInsufficientInventory)
		available, err := uc.HasProductInventory(context.Background(), ProductInventoryAvailabilityRequest{ProductID: 5, ProjectID: 4, EmailSuffix: suffix, PublicOnly: true})
		require.NoError(t, err)
		require.False(t, available)
		require.Empty(t, repo.scopes, "cold snapshots must not fan out into live stock queries")
		available, known := productInventoryAvailable(&ProjectProductInventoryTotals{Items: []ProductInventoryTotal{{ProductID: 5, ProductType: provider, TotalAvailable: 5, PublicAvailable: 5}}}, ProductInventoryAvailabilityRequest{ProductID: 5, EmailSuffix: suffix, PublicOnly: true})
		require.False(t, available)
		require.False(t, known, "a missing suffix in a warm legacy snapshot is unknown")
	}
}
