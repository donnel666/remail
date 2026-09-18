package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
)

type icloudProbeRepo struct {
	*allocationLockRepo
	batches          [][]uint16
	scopes           []domain.SupplyScope
	limits           []int
	retryTransaction bool
	retried          bool
}

func (*icloudProbeRepo) HasParentTx(context.Context) bool { return false }

func (r *icloudProbeRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	err := r.allocationLockRepo.WithTx(ctx, fn)
	if err != nil && r.retryTransaction && !r.retried {
		r.retried = true
		return r.allocationLockRepo.WithTx(ctx, fn)
	}
	return err
}

func (r *icloudProbeRepo) ListICloudSourceCandidates(_ context.Context, _ uint, _ uint, scope domain.SupplyScope, buckets []uint16, limit int) ([]uint, error) {
	r.batches = append(r.batches, slices.Clone(buckets))
	r.scopes = append(r.scopes, scope)
	r.limits = append(r.limits, limit)
	if r.noCandidates || r.emptyScope == scope {
		return nil, nil
	}
	var ids []uint
	for _, bucket := range buckets {
		if !r.emptyBuckets[bucket] {
			ids = append(ids, uint(bucket)+1)
			if len(ids) == limit {
				break
			}
		}
	}
	return ids, nil
}

func (r *icloudProbeRepo) LockICloudCandidate(_ context.Context, resourceID uint, _ uint, _ uint, _ domain.SupplyScope) (*ICloudCandidate, error) {
	if r.candidateUnavailable[resourceID] {
		return nil, nil
	}
	return &ICloudCandidate{ResourceID: resourceID, AliasID: resourceID, Email: fmt.Sprintf("alias-%d@icloud.com", resourceID)}, nil
}

func (r *icloudProbeRepo) CreateICloudAllocation(_ context.Context, allocation *domain.ICloudAllocation) error {
	r.creates++
	if r.writeConflict {
		return domain.ErrAllocationConflict
	}
	allocation.ID = uint(r.creates)
	return nil
}

func (*icloudProbeRepo) TouchICloudAllocated(context.Context, uint, uint, time.Time) error {
	return nil
}

func TestICloudProbeBudgetSurvivesMissesAndRetries(t *testing.T) {
	previous := runtimeconfig.String("bucket_probe_count", "4")
	runtimeconfig.Set("bucket_probe_count", "4")
	t.Cleanup(func() { runtimeconfig.Set("bucket_probe_count", previous) })
	orderNo := ""
	for i := 0; i < 10000; i++ {
		candidate := fmt.Sprintf("icloud-wrap-%d", i)
		if bucketProbeSequence(candidate, 4, "icloud", ICloudBucketCount)[0] == ICloudBucketCount-1 {
			orderNo = candidate
			break
		}
	}
	if orderNo == "" {
		t.Fatal("missing wraparound fixture")
	}
	for _, name := range []string{"empty", "busy", "no aliases", "write conflict", "expanded write conflict", "transaction retry", "canceled"} {
		t.Run(name, func(t *testing.T) {
			base := &allocationLockRepo{
				config:          ProductAllocationConfig{ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeICloud},
				noCandidates:    name == "empty",
				rootUnavailable: make(map[uint]bool), candidateUnavailable: make(map[uint]bool),
			}
			for bucket := uint(0); bucket < ICloudBucketCount; bucket++ {
				base.rootUnavailable[bucket+1] = name == "busy"
				base.candidateUnavailable[bucket+1] = name == "no aliases"
			}
			wantErr := domain.ErrInsufficientInventory
			if name == "busy" || name == "write conflict" || name == "expanded write conflict" || name == "transaction retry" {
				wantErr = domain.ErrAllocationConflict
				base.writeConflict = name != "busy"
			}
			if name == "expanded write conflict" {
				base.emptyBuckets = make(map[uint16]bool)
				last := uint16((ICloudBucketCount - 1 + 4 + ICloudExpansionBuckets - 1) % ICloudBucketCount)
				for bucket := uint16(0); bucket < ICloudBucketCount; bucket++ {
					base.emptyBuckets[bucket] = bucket != last
				}
			}
			repo := &icloudProbeRepo{allocationLockRepo: base, retryTransaction: name == "transaction retry"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				wantErr = context.Canceled
				cancel()
			}
			result, err := NewUseCase(repo).Allocate(ctx, AllocateCommand{
				OrderNo: orderNo, BuyerUserID: 3, ProjectProductID: 5, SupplyScope: domain.SupplyScopePublic,
			})
			if result != nil || !errors.Is(err, wantErr) || errors.Is(err, domain.ErrDefinitiveInventoryExhausted) {
				t.Fatalf("Allocate() = %#v, %v; want %v without a global exhaustion claim", result, err, wantErr)
			}
			if name == "canceled" {
				if len(repo.batches) != 0 {
					t.Fatalf("canceled allocation queried %d batches", len(repo.batches))
				}
				return
			}
			if len(repo.batches) != 5 || len(repo.batches[4]) != ICloudExpansionBuckets {
				t.Fatalf("batches = %v; want four single buckets and one batch of 100", repo.batches)
			}
			var seen []uint16
			for i, batch := range repo.batches {
				wantLimit := candidateWindowSizeValue()
				if i == 4 {
					wantLimit = globalCandidateWindowValue()
				} else if len(batch) != 1 {
					t.Fatalf("initial batch %d contains %d buckets", i, len(batch))
				}
				if repo.limits[i] != wantLimit {
					t.Fatalf("batch %d limit = %d, want %d", i, repo.limits[i], wantLimit)
				}
				seen = append(seen, batch...)
			}
			for i, bucket := range seen {
				if want := uint16((ICloudBucketCount - 1 + i) % ICloudBucketCount); bucket != want {
					t.Fatalf("probe %d = %d, want %d", i, bucket, want)
				}
			}
			if base.waiting != 0 {
				t.Fatalf("iCloud waited on %d resource roots", base.waiting)
			}
		})
	}
}

func TestICloudProbeBudgetsKeepSupplyScopesSeparate(t *testing.T) {
	repo := &icloudProbeRepo{allocationLockRepo: &allocationLockRepo{
		config:     ProductAllocationConfig{ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeICloud},
		emptyScope: domain.SupplyScopeOwned,
	}}
	result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
		OrderNo: "icloud-separate-scopes", BuyerUserID: 3, ProjectProductID: 5,
		SupplyScopes: []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic},
	})
	if err != nil || result == nil || result.SupplyScope != domain.SupplyScopePublic {
		t.Fatalf("Allocate() = %#v, %v; want public supply after the owned window", result, err)
	}
	if len(repo.batches) != bucketProbeCountValue()+2 || repo.scopes[len(repo.scopes)-1] != domain.SupplyScopePublic {
		t.Fatalf("unexpected per-scope probe windows: %v %v", repo.batches, repo.scopes)
	}
}
