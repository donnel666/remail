package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
)

// stubWalletRepo satisfies WalletRepository through the embedded (nil)
// interface; only AdjustConsumerBalance is implemented, which is all
// BulkAdjustConsumer exercises.
type stubWalletRepo struct {
	WalletRepository
	adjust func(AdjustConsumerBalanceCommand) (*AdjustBalanceResult, error)
}

func (s stubWalletRepo) AdjustConsumerBalance(_ context.Context, req AdjustConsumerBalanceCommand) (*AdjustBalanceResult, error) {
	return s.adjust(req)
}

func TestBulkAdjustConsumer(t *testing.T) {
	var gotDirection domain.TransactionDirection
	repo := stubWalletRepo{adjust: func(req AdjustConsumerBalanceCommand) (*AdjustBalanceResult, error) {
		gotDirection = req.Direction
		if req.UserID == 2 { // one user cannot cover the debit
			return nil, domain.ErrInsufficientBalance
		}
		return &AdjustBalanceResult{}, nil
	}}
	uc := NewWalletUseCase(repo)

	// Negative amount debits; user 2 is skipped, the other two affected.
	affected, skipped, err := uc.BulkAdjustConsumer(context.Background(), []uint{1, 2, 3}, "-10.00", "test", "idem-1", "req-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDirection != domain.TransactionDirectionOut {
		t.Fatalf("negative amount should debit, got direction %q", gotDirection)
	}
	if affected != 2 || skipped != 1 {
		t.Fatalf("want affected=2 skipped=1, got affected=%d skipped=%d", affected, skipped)
	}

	// Positive amount credits.
	if _, _, err := uc.BulkAdjustConsumer(context.Background(), []uint{1}, "5", "test", "idem-2", "req-2", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDirection != domain.TransactionDirectionIn {
		t.Fatalf("positive amount should credit, got direction %q", gotDirection)
	}

	// Zero amount is rejected before touching the repo.
	if _, _, err := uc.BulkAdjustConsumer(context.Background(), []uint{1}, "0", "test", "idem-3", "req-3", nil); err == nil {
		t.Fatalf("zero amount should error")
	}
}
