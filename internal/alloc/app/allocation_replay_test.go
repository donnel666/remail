package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
)

type allocationReplayRaceRepo struct {
	*allocationLockRepo
	winner      *domain.UnifiedAllocation
	scanned     bool
	parentTx    bool
	scanErr     error
	lookupErr   error
	rollbackErr error
	replayReads int
	replayInTx  bool
}

func (r *allocationReplayRaceRepo) HasParentTx(context.Context) bool { return r.parentTx }

func (r *allocationReplayRaceRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	err := r.allocationLockRepo.WithTx(ctx, fn)
	if err != nil && r.rollbackErr != nil {
		return errors.Join(err, r.rollbackErr)
	}
	return err
}

func (r *allocationReplayRaceRepo) FindExistingAllocation(_ context.Context, orderNo string) (*domain.UnifiedAllocation, error) {
	r.finds++
	if r.scanned {
		r.replayReads++
		r.replayInTx = r.replayInTx || r.txActive
		if r.lookupErr != nil {
			return nil, r.lookupErr
		}
		if r.winner != nil && r.winner.OrderNo == orderNo {
			return r.winner, nil
		}
	}
	return nil, nil
}

func (r *allocationReplayRaceRepo) ListMicrosoftSourceCandidates(ctx context.Context, projectID, buyerID uint, scope domain.SupplyScope, mailbox domain.MicrosoftMailbox, bucket *uint16, limit int, suffix string) ([]MicrosoftCandidate, error) {
	// The winner commits after this request's initial idempotency read. Its
	// allocation consumes the only mailbox, so even the global scan is empty.
	r.scanned = true
	if r.scanErr != nil {
		return nil, r.scanErr
	}
	return r.allocationLockRepo.ListMicrosoftSourceCandidates(ctx, projectID, buyerID, scope, mailbox, bucket, limit, suffix)
}

func TestAllocationReplayAfterConcurrentWinnerCommit(t *testing.T) {
	winner := &domain.UnifiedAllocation{
		Type: domain.AllocationTypeMicrosoft, ID: 42, OrderNo: "same-order",
		ProjectID: 4, ProductID: 5, ResourceID: 7, Email: "only@example.com",
		SupplyScope: domain.SupplyScopePublic, Status: domain.AllocationStatusAllocated,
	}
	queryErr := errors.New("candidate query failed")
	lookupErr := errors.New("winner query failed")
	rollbackErr := errors.New("rollback hook failed")
	releasedStatus, releasedTimestamp := *winner, *winner
	releasedStatus.Status = domain.AllocationStatusReleased
	releasedAt := time.Unix(1, 0).UTC()
	releasedTimestamp.ReleasedAt = &releasedAt
	for _, test := range []struct {
		name        string
		winner      *domain.UnifiedAllocation
		parentTx    bool
		scanErr     error
		lookupErr   error
		rollbackErr error
		wantErr     error
	}{
		{name: "reuse winner after definitive exhaustion", winner: winner},
		{name: "reuse winner after temporary inventory miss", winner: winner, scanErr: domain.ErrInsufficientInventory},
		{name: "reuse winner after allocation conflict", winner: winner, scanErr: domain.ErrAllocationConflict},
		{name: "preserve genuine exhaustion", wantErr: domain.ErrDefinitiveInventoryExhausted},
		{name: "parent transaction must retain its failure", winner: winner, parentTx: true, wantErr: domain.ErrDefinitiveInventoryExhausted},
		{name: "parent transaction must retain allocation conflict", winner: winner, parentTx: true, scanErr: domain.ErrAllocationConflict, wantErr: domain.ErrAllocationConflict},
		{name: "do not hide unrelated query failure", winner: winner, scanErr: queryErr, wantErr: queryErr},
		{name: "winner lookup failure stays an infrastructure error", winner: winner, lookupErr: lookupErr, wantErr: lookupErr},
		{name: "released status is not a reusable winner", winner: &releasedStatus, wantErr: domain.ErrDefinitiveInventoryExhausted},
		{name: "release timestamp is not a reusable winner", winner: &releasedTimestamp, wantErr: domain.ErrDefinitiveInventoryExhausted},
		{name: "joined rollback failure must not be hidden", winner: winner, rollbackErr: rollbackErr, wantErr: rollbackErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &allocationReplayRaceRepo{
				allocationLockRepo: &allocationLockRepo{
					config:       ProductAllocationConfig{ProjectID: 4, ProductID: 5, ProductType: coredomain.ProductTypeMicrosoft, MainWeight: 1},
					noCandidates: true,
				},
				winner: test.winner, parentTx: test.parentTx, scanErr: test.scanErr, lookupErr: test.lookupErr, rollbackErr: test.rollbackErr,
			}
			result, err := NewUseCase(repo).Allocate(context.Background(), AllocateCommand{
				OrderNo: winner.OrderNo, BuyerUserID: 3, ProjectProductID: winner.ProductID,
				SupplyScope: domain.SupplyScopePublic,
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Allocate() error = %v, want %v; idempotency reads = %d", err, test.wantErr, repo.finds)
			}
			// Temporary misses/conflicts may use the existing fresh-transaction
			// retry. Definitive exhaustion has no retry: its fallback must run
			// after the failed transaction has ended, with txActive=false.
			if test.scanErr == nil && repo.replayInTx {
				t.Fatal("winner fallback ran before the failed transaction ended")
			}
			if (test.parentTx || test.rollbackErr != nil || test.scanErr == queryErr) && repo.replayReads != 0 {
				t.Fatalf("unexpected winner lookup after a protected failure: %d", repo.replayReads)
			}
			if test.lookupErr != nil && errors.Is(err, domain.ErrInsufficientInventory) {
				t.Fatalf("winner lookup failure was classified as business inventory exhaustion: %v", err)
			}
			if test.rollbackErr != nil && !errors.Is(err, domain.ErrDefinitiveInventoryExhausted) {
				t.Fatalf("joined error lost its original allocation failure: %v", err)
			}
			if test.wantErr != nil {
				if result != nil {
					t.Fatalf("Allocate() returned allocation %#v for a failed attempt", result)
				}
			} else if result == nil || result.ID != winner.ID || result.OrderNo != winner.OrderNo || result.ResourceID != winner.ResourceID || result.Created {
				t.Fatalf("Allocate() = %#v, want existing winner %#v without creating another allocation", result, winner)
			}
			if repo.creates != 0 {
				t.Fatalf("allocation creates = %d, want zero for an idempotent replay", repo.creates)
			}
		})
	}
}
