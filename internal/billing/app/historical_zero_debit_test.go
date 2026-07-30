package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

type historicalZeroDebitRepoStub struct {
	WalletRepository
	command AdjustConsumerBalanceCommand
}

func (s *historicalZeroDebitRepoStub) RecordHistoricalZeroDebit(_ context.Context, command AdjustConsumerBalanceCommand) (*domain.Transaction, error) {
	s.command = command
	return &domain.Transaction{ID: 9}, nil
}

func TestRecordHistoricalZeroDebitUsesDedicatedRepositoryPath(t *testing.T) {
	repo := &historicalZeroDebitRepoStub{}
	result, err := NewWalletUseCase(repo).RecordHistoricalZeroDebit(context.Background(), AdjustConsumerBalanceRequest{
		UserID: 1, Amount: "0", Reason: "order:HIST-1", IdempotencyKey: "history:HIST-1:debit",
	})

	require.NoError(t, err)
	require.Equal(t, uint(9), result.ID)
	require.Equal(t, "order:HIST-1", repo.command.Reason)
	require.Equal(t, "history:HIST-1:debit", repo.command.IdempotencyKey)
}

func TestRecordHistoricalZeroDebitRejectsNonZeroAmount(t *testing.T) {
	_, err := NewWalletUseCase(&historicalZeroDebitRepoStub{}).RecordHistoricalZeroDebit(context.Background(), AdjustConsumerBalanceRequest{
		UserID: 1, Amount: "0.01", Reason: "order:HIST-1", IdempotencyKey: "history:HIST-1:debit",
	})

	require.ErrorIs(t, err, domain.ErrInvalidAmount)
}
