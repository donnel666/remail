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
	var gotClampToBalance bool
	repo := stubWalletRepo{adjust: func(req AdjustConsumerBalanceCommand) (*AdjustBalanceResult, error) {
		gotDirection = req.Direction
		gotClampToBalance = req.ClampToBalance
		if req.UserID == 2 && !req.ClampToBalance { // non-bulk debits still reject overdrafts
			return nil, domain.ErrInsufficientBalance
		}
		return &AdjustBalanceResult{}, nil
	}}
	uc := NewWalletUseCase(repo)

	// Negative amount debits use the partial-debit path for bulk clearing.
	affected, skipped, err := uc.BulkAdjustConsumer(context.Background(), []uint{1, 2, 3}, "-10.00", "test", "idem-1", "req-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDirection != domain.TransactionDirectionOut {
		t.Fatalf("negative amount should debit, got direction %q", gotDirection)
	}
	if !gotClampToBalance {
		t.Fatal("bulk debit should clamp to the current balance")
	}
	if affected != 3 || skipped != 0 {
		t.Fatalf("want affected=3 skipped=0, got affected=%d skipped=%d", affected, skipped)
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

func TestGrantRegistrationRewardBuildsIdempotentCredit(t *testing.T) {
	var commands []AdjustConsumerBalanceCommand
	repo := stubWalletRepo{adjust: func(req AdjustConsumerBalanceCommand) (*AdjustBalanceResult, error) {
		commands = append(commands, req)
		return &AdjustBalanceResult{}, nil
	}}
	uc := NewWalletUseCase(repo)

	for i := 0; i < 2; i++ {
		if err := uc.GrantRegistrationReward(context.Background(), 42, "12.340000"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if len(commands) != 2 {
		t.Fatalf("want two commands, got %d", len(commands))
	}
	first, second := commands[0], commands[1]
	if first.Amount != "12.34" || first.TransactionType != domain.TransactionTypeCredit || first.Direction != domain.TransactionDirectionIn {
		t.Fatalf("unexpected registration reward command: %+v", first)
	}
	if first.BizType != "registration_reward" || first.IdempotencyKey != "registration_reward:42" {
		t.Fatalf("unexpected registration reward identity: %+v", first)
	}
	if first.IdempotencyKey != second.IdempotencyKey || first.RequestFingerprint != second.RequestFingerprint {
		t.Fatalf("registration reward retry must be stable: first=%+v second=%+v", first, second)
	}
	if err := uc.GrantRegistrationReward(context.Background(), 42, "0"); err == nil {
		t.Fatal("zero registration reward should be rejected")
	}
	if len(commands) != 2 {
		t.Fatal("invalid reward must not reach the wallet repository")
	}
}
