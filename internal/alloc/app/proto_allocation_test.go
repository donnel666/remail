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

type protoExistingAllocationRepo struct {
	*allocationLockRepo
	existing  domain.UnifiedAllocation
	missFirst bool
	unready   bool
	readyErr  error
}

func (r *protoExistingAllocationRepo) FindExistingAllocation(context.Context, string) (*domain.UnifiedAllocation, error) {
	r.finds++
	if r.missFirst && r.finds == 1 {
		return nil, nil
	}
	result := r.existing
	return &result, nil
}

func (r *protoExistingAllocationRepo) ProtoAllocationReady(context.Context, string, uint) (bool, error) {
	return !r.unready && r.readyErr == nil, r.readyErr
}

type protoAllocationLockRepo struct {
	*allocationLockRepo
	protoCandidates []ProtoCandidate
	listedSuffixes  []string
	lockedSuffixes  []string
	created         *domain.ProtoAllocation
}

func (r *protoAllocationLockRepo) ListProtoSourceCandidates(_ context.Context, _, _ uint, _ domain.SupplyScope, _ *uint16, _ int, suffix string) ([]ProtoCandidate, error) {
	r.listedSuffixes = append(r.listedSuffixes, suffix)
	var candidates []ProtoCandidate
	for _, candidate := range r.protoCandidates {
		if suffix == "" || strings.HasSuffix(candidate.Email, "@"+suffix) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func (r *protoAllocationLockRepo) LockProtoCandidate(_ context.Context, resourceID, _, _ uint, _ domain.SupplyScope, suffix string) (*ProtoCandidate, error) {
	r.lockedSuffixes = append(r.lockedSuffixes, suffix)
	for _, candidate := range r.protoCandidates {
		if candidate.ResourceID == resourceID && (suffix == "" || strings.HasSuffix(candidate.Email, "@"+suffix)) {
			return &candidate, nil
		}
	}
	return nil, nil
}

func (r *protoAllocationLockRepo) CreateProtoAllocation(_ context.Context, allocation *domain.ProtoAllocation) error {
	allocation.ID = 1
	if allocation.CreatedAt.IsZero() {
		allocation.CreatedAt = time.Now().UTC()
	}
	r.created = allocation
	return nil
}

func (*protoAllocationLockRepo) TouchProtoAllocated(context.Context, uint, time.Time) error {
	return nil
}

func TestProtoAllocationUsesUnifiedOrderGuardAndMainMailbox(t *testing.T) {
	repo := &protoAllocationLockRepo{
		allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeProto,
			CodeEnabled: true, CodeSupplierPrice: "0.25", MainWeight: 1,
		}},
		protoCandidates: []ProtoCandidate{{ResourceID: 9, Email: "one@proton.me"}},
	}
	uc := NewUseCase(repo)
	uc.SetProtoProtocolReady(true)
	result, err := uc.Allocate(context.Background(), AllocateCommand{
		OrderNo: "proto-order-1", BuyerUserID: 3, ProjectProductID: 5,
		ServiceMode: domain.GmailServiceModeCode, SupplyScope: domain.SupplyScopePublic,
	})
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if result == nil || result.Type != domain.AllocationTypeProto || result.ResourceID != 9 || result.Mailbox != "main" {
		t.Fatalf("Allocate() = %#v, want Proto main allocation", result)
	}
	if repo.created == nil || repo.created.OrderNo != "proto-order-1" || repo.created.CostPointsSnapshot != "0.25" {
		t.Fatalf("created allocation = %#v, want order and cost snapshot", repo.created)
	}
}

func TestProtoProtocolGateBlocksNormalCandidatesAndInventory(t *testing.T) {
	repo := &protoAllocationLockRepo{
		allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
			ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeProto,
			CodeEnabled: true, CodeSupplierPrice: "0.25", MainWeight: 1,
		}},
		protoCandidates: []ProtoCandidate{{ResourceID: 9, OwnerUserID: 1, Email: "one@example.com"}},
	}
	uc := NewUseCase(repo)
	_, err := uc.Allocate(context.Background(), AllocateCommand{
		OrderNo: "proto-disabled", BuyerUserID: 3, ProjectProductID: 5,
		ServiceMode: domain.ServiceModeCode, SupplyScope: domain.SupplyScopePublic,
	})
	if err != domain.ErrInsufficientInventory || repo.created != nil {
		t.Fatalf("unconfigured protocol allocated resource: %v %#v", err, repo.created)
	}
	snapshot := &ProjectProductInventoryTotals{ProjectID: 4, TotalAvailable: 7, Items: []ProductInventoryTotal{
		{ProductID: 5, ProductType: coredomain.ProductTypeProto, TotalAvailable: 2, PublicAvailable: 2},
		{ProductID: 6, ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: 5, PublicAvailable: 5},
	}}
	visible := uc.protoInventoryTotals(snapshot)
	if visible.TotalAvailable != 5 || visible.Items[0].TotalAvailable != 0 || visible.Items[1].TotalAvailable != 5 || snapshot.TotalAvailable != 7 {
		t.Fatalf("protocol gate corrupted inventory: visible=%#v source=%#v", visible, snapshot)
	}
	uc.SetProtoProtocolReady(true)
	if uc.protoInventoryTotals(snapshot).TotalAvailable != 7 {
		t.Fatal("configured protocol did not restore authoritative inventory")
	}
}

func TestProtoProtocolGateCoversExistingAllocationPaths(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		missFirst    bool
	}{
		{name: "ordinary"},
		{name: "random-selector", suffix: "outlook"},
		{name: "random-selector-recheck", suffix: "outlook", missFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &protoExistingAllocationRepo{
				allocationLockRepo: &allocationLockRepo{config: ProductAllocationConfig{
					ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeProto,
				}},
				existing: domain.UnifiedAllocation{Type: domain.AllocationTypeProto, ID: 8, OrderNo: "PROTO-OLD",
					ResourceID: 9, ProjectID: 4, ProductID: 5, Status: domain.AllocationStatusAllocated},
				missFirst: tc.missFirst,
			}
			uc := NewUseCase(repo)
			cmd := AllocateCommand{OrderNo: "PROTO-OLD", BuyerUserID: 3, ProjectProductID: 5,
				ServiceMode: domain.ServiceModePurchase, EmailSuffix: tc.suffix, FulfillExistingOrder: true}
			result, err := uc.Allocate(context.Background(), cmd)
			if !errors.Is(err, domain.ErrInsufficientInventory) || result != nil {
				t.Fatalf("existing Proto allocation bypassed protocol gate: result=%#v err=%v", result, err)
			}
			uc.SetProtoProtocolReady(true)
			repo.unready = true
			result, err = uc.Allocate(context.Background(), cmd)
			if !errors.Is(err, domain.ErrInsufficientInventory) || result != nil {
				t.Fatalf("existing Proto allocation without a current session was reused: result=%#v err=%v", result, err)
			}
			repo.unready = false
			repo.readyErr = errors.New("readiness database is unavailable")
			result, err = uc.Allocate(context.Background(), cmd)
			if !errors.Is(err, repo.readyErr) || result != nil {
				t.Fatalf("Proto readiness error was swallowed: result=%#v err=%v", result, err)
			}
			repo.readyErr = nil
			result, err = uc.Allocate(context.Background(), cmd)
			if err != nil || result == nil || result.ID != 8 {
				t.Fatalf("ready Proto allocation could not resume: result=%#v err=%v", result, err)
			}
		})
	}
	for _, typ := range []domain.AllocationType{domain.AllocationTypeMicrosoft, domain.AllocationTypeDomain, domain.AllocationTypeGmail, domain.AllocationTypeICloud} {
		repo := &protoExistingAllocationRepo{allocationLockRepo: &allocationLockRepo{},
			existing: domain.UnifiedAllocation{Type: typ, ID: 8}}
		result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{OrderNo: "OLD", BuyerUserID: 3, ProjectProductID: 5})
		if err != nil || result == nil || result.Type != typ {
			t.Fatalf("Proto gate affected %s: result=%#v err=%v", typ, result, err)
		}
	}
}
