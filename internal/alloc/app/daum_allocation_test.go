package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
)

type daumExistingAllocationRepo struct {
	*allocationLockRepo
	existing  domain.UnifiedAllocation
	missFirst bool
	unready   bool
	readyErr  error
}

func (r *daumExistingAllocationRepo) FindExistingAllocation(context.Context, string) (*domain.UnifiedAllocation, error) {
	r.finds++
	if r.missFirst && r.finds == 1 {
		return nil, nil
	}
	result := r.existing
	return &result, nil
}

func (r *daumExistingAllocationRepo) DaumAllocationReady(context.Context, string, uint) (bool, error) {
	return !r.unready && r.readyErr == nil, r.readyErr
}

type daumAllocationLockRepo struct {
	*allocationLockRepo
	daumCandidates []DaumCandidate
	listedSuffixes []string
	lockedSuffixes []string
	created        *domain.DaumAllocation
}

func (r *daumAllocationLockRepo) ListDaumSourceCandidates(_ context.Context, _, _ uint, _ domain.SupplyScope, _ []uint16, _ int, suffix string) ([]DaumCandidate, error) {
	r.listedSuffixes = append(r.listedSuffixes, suffix)
	var candidates []DaumCandidate
	for _, candidate := range r.daumCandidates {
		if suffix == "" || strings.HasSuffix(candidate.Email, "@"+suffix) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func (r *daumAllocationLockRepo) LockDaumCandidate(_ context.Context, selected DaumCandidate, _, _ uint, _ domain.SupplyScope, suffix string) (*DaumCandidate, error) {
	r.lockedSuffixes = append(r.lockedSuffixes, suffix)
	for _, candidate := range r.daumCandidates {
		if candidate.ResourceID == selected.ResourceID && candidate.Email == selected.Email && (suffix == "" || strings.HasSuffix(candidate.Email, "@"+suffix)) {
			return &candidate, nil
		}
	}
	return nil, nil
}

func (r *daumAllocationLockRepo) CreateDaumAllocation(_ context.Context, allocation *domain.DaumAllocation) error {
	allocation.ID = 1
	if allocation.CreatedAt.IsZero() {
		allocation.CreatedAt = time.Now().UTC()
	}
	r.created = allocation
	return nil
}

func (*daumAllocationLockRepo) TouchDaumAllocated(context.Context, uint, time.Time) error {
	return nil
}

func TestDaumAllocationUsesUnifiedOrderGuardAndMainMailbox(t *testing.T) {
	repo := &daumAllocationLockRepo{
		allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeDaum,
			CodeEnabled: true, CodeSupplierPrice: "0.25", MainWeight: 1,
		}},
		daumCandidates: []DaumCandidate{{ResourceID: 9, Mailbox: "main", Email: "one@daum.net"}},
	}
	uc := NewUseCase(repo)
	uc.SetDaumProtocolReady(true)
	result, err := uc.Allocate(context.Background(), AllocateCommand{
		OrderNo: "daum-order-1", BuyerUserID: 3, ProjectProductID: 5,
		ServiceMode: domain.GmailServiceModeCode, SupplyScope: domain.SupplyScopePublic,
	})
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if result == nil || result.Type != domain.AllocationTypeDaum || result.ResourceID != 9 || result.Mailbox != "main" {
		t.Fatalf("Allocate() = %#v, want Daum main allocation", result)
	}
	if repo.created == nil || repo.created.OrderNo != "daum-order-1" || repo.created.CostPointsSnapshot != "0.25" {
		t.Fatalf("created allocation = %#v, want order and cost snapshot", repo.created)
	}
}

func TestDaumProtocolGateBlocksNormalCandidatesAndInventory(t *testing.T) {
	repo := &daumAllocationLockRepo{
		allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeDaum,
			CodeEnabled: true, CodeSupplierPrice: "0.25", MainWeight: 1,
		}},
		daumCandidates: []DaumCandidate{{ResourceID: 9, OwnerUserID: 1, Email: "one@example.com"}},
	}
	uc := NewUseCase(repo)
	_, err := uc.Allocate(context.Background(), AllocateCommand{
		OrderNo: "daum-disabled", BuyerUserID: 3, ProjectProductID: 5,
		ServiceMode: domain.ServiceModeCode, SupplyScope: domain.SupplyScopePublic,
	})
	if err != domain.ErrInsufficientInventory || repo.created != nil {
		t.Fatalf("unconfigured protocol allocated resource: %v %#v", err, repo.created)
	}
	snapshot := &ProjectProductInventoryTotals{ProjectID: 4, TotalAvailable: 7, Items: []ProductInventoryTotal{
		{ProductID: 5, ProductType: coredomain.ProductTypeDaum, TotalAvailable: 2, PublicAvailable: 2},
		{ProductID: 6, ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: 5, PublicAvailable: 5},
	}}
	visible := uc.daumInventoryTotals(snapshot)
	if visible.TotalAvailable != 5 || visible.Items[0].TotalAvailable != 0 || visible.Items[1].TotalAvailable != 5 || snapshot.TotalAvailable != 7 {
		t.Fatalf("protocol gate corrupted inventory: visible=%#v source=%#v", visible, snapshot)
	}
	uc.SetDaumProtocolReady(true)
	if uc.daumInventoryTotals(snapshot).TotalAvailable != 7 {
		t.Fatal("configured protocol did not restore authoritative inventory")
	}
}

func TestDaumProtocolGateCoversExistingAllocationPaths(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		missFirst    bool
	}{
		{name: "ordinary"},
		{name: "random-selector", suffix: "outlook"},
		{name: "random-selector-recheck", suffix: "outlook", missFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &daumExistingAllocationRepo{
				allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
					ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeDaum,
				}},
				existing: domain.UnifiedAllocation{Type: domain.AllocationTypeDaum, ID: 8, OrderNo: "DAUM-OLD",
					ResourceID: 9, ProjectID: 4, ProductID: 5, Status: domain.AllocationStatusAllocated},
				missFirst: tc.missFirst,
			}
			uc := NewUseCase(repo)
			cmd := AllocateCommand{OrderNo: "DAUM-OLD", BuyerUserID: 3, ProjectProductID: 5,
				ServiceMode: domain.ServiceModePurchase, EmailSuffix: tc.suffix, FulfillExistingOrder: true}
			result, err := uc.Allocate(context.Background(), cmd)
			if !errors.Is(err, domain.ErrInsufficientInventory) || result != nil {
				t.Fatalf("existing Daum allocation bypassed protocol gate: result=%#v err=%v", result, err)
			}
			uc.SetDaumProtocolReady(true)
			repo.unready = true
			result, err = uc.Allocate(context.Background(), cmd)
			if !errors.Is(err, domain.ErrInsufficientInventory) || result != nil {
				t.Fatalf("existing Daum allocation with an unavailable resource was reused: result=%#v err=%v", result, err)
			}
			repo.unready = false
			repo.readyErr = errors.New("readiness database is unavailable")
			result, err = uc.Allocate(context.Background(), cmd)
			if !errors.Is(err, repo.readyErr) || result != nil {
				t.Fatalf("Daum readiness error was swallowed: result=%#v err=%v", result, err)
			}
			repo.readyErr = nil
			result, err = uc.Allocate(context.Background(), cmd)
			if err != nil || result == nil || result.ID != 8 {
				t.Fatalf("ready Daum allocation could not resume: result=%#v err=%v", result, err)
			}
		})
	}
	for _, typ := range []domain.AllocationType{domain.AllocationTypeMicrosoft, domain.AllocationTypeDomain, domain.AllocationTypeGmail, domain.AllocationTypeICloud} {
		repo := &daumExistingAllocationRepo{allocationLockRepo: &allocationLockRepo{},
			existing: domain.UnifiedAllocation{Type: typ, ID: 8}}
		result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{OrderNo: "OLD", BuyerUserID: 3, ProjectProductID: 5})
		if err != nil || result == nil || result.Type != typ {
			t.Fatalf("Daum gate affected %s: result=%#v err=%v", typ, result, err)
		}
	}
}
