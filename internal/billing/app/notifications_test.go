package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
)

type balanceWarningRepoStub struct {
	WalletRepository
	claims   []BalanceWarningClaim
	released []BalanceWarningClaim
}

func (s *balanceWarningRepoStub) ClaimBalanceWarnings(context.Context, int) ([]BalanceWarningClaim, error) {
	if len(s.claims) == 0 {
		return nil, nil
	}
	claim := s.claims[0]
	s.claims = s.claims[1:]
	return []BalanceWarningClaim{claim}, nil
}

func (s *balanceWarningRepoStub) ReleaseBalanceWarning(_ context.Context, claim BalanceWarningClaim) error {
	s.released = append(s.released, claim)
	return nil
}

type balanceWarningDirectoryStub struct{ users map[uint]UserDirectoryEntry }

func (s balanceWarningDirectoryStub) LookupUsers(context.Context, []uint) (map[uint]UserDirectoryEntry, error) {
	return s.users, nil
}

func (balanceWarningDirectoryStub) ListUsers(context.Context, UserDirectoryQuery) (UserDirectoryPage, error) {
	return UserDirectoryPage{}, nil
}

type balanceWarningDeliveryStub struct{ messages []maildomain.OutboundMessage }

func (s *balanceWarningDeliveryStub) Send(_ context.Context, message maildomain.OutboundMessage) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestBalanceWarningDispatchSendsEveryNewTierOnce(t *testing.T) {
	repo := &balanceWarningRepoStub{claims: []BalanceWarningClaim{
		{UserID: 7, Balance: "400.00", Cycle: 3, PreviousLevel: 0, Level: 1},
		{UserID: 7, Balance: "400.00", Cycle: 3, PreviousLevel: 1, Level: 2},
		{UserID: 7, Balance: "400.00", Cycle: 3, PreviousLevel: 2, Level: 3},
		{UserID: 7, Balance: "400.00", Cycle: 3, PreviousLevel: 3, Level: 4},
	}}
	delivery := &balanceWarningDeliveryStub{}
	uc := NewWalletUseCase(repo)
	uc.SetUserDirectory(balanceWarningDirectoryStub{users: map[uint]UserDirectoryEntry{7: {UserID: 7, Email: "user@example.com", Status: "active"}}})
	uc.SetMailDelivery(delivery)

	for range 4 {
		require.NoError(t, uc.DispatchBalanceWarnings(context.Background(), 100))
	}
	require.Len(t, delivery.messages, 4)
	require.Contains(t, delivery.messages[0].TextBody, "≤3000.00 积分")
	require.Contains(t, delivery.messages[3].TextBody, "≤500.00 积分")
	require.Empty(t, repo.released)

	require.NoError(t, uc.DispatchBalanceWarnings(context.Background(), 100))
	require.Len(t, delivery.messages, 4)
}

func TestBalanceWarningDispatchSkipsInactiveUsers(t *testing.T) {
	repo := &balanceWarningRepoStub{claims: []BalanceWarningClaim{
		{UserID: 7, Balance: "1.00", Cycle: 1, PreviousLevel: 2, Level: 3},
		{UserID: 8, Balance: "1.00", Cycle: 1, PreviousLevel: 2, Level: 3},
	}}
	delivery := &balanceWarningDeliveryStub{}
	uc := NewWalletUseCase(repo)
	uc.SetUserDirectory(balanceWarningDirectoryStub{users: map[uint]UserDirectoryEntry{
		7: {UserID: 7, Email: "disabled@example.com", Status: "disabled"},
		8: {UserID: 8, Email: "deleted@example.com", Status: "deleted"},
	}})
	uc.SetMailDelivery(delivery)

	require.NoError(t, uc.DispatchBalanceWarnings(context.Background(), 100))
	require.NoError(t, uc.DispatchBalanceWarnings(context.Background(), 100))
	require.Empty(t, delivery.messages)
}

type cardRedeemRepoStub struct {
	WalletRepository
	result *RedeemCardResult
}

func (s *cardRedeemRepoStub) RedeemCard(context.Context, RedeemCardCommand) (*RedeemCardResult, error) {
	return s.result, nil
}

func TestRedeemCardSendsCreditedNotificationOnlyForFirstApplication(t *testing.T) {
	repo := &cardRedeemRepoStub{result: &RedeemCardResult{
		Wallet:      domain.Wallet{UserID: 7, ConsumerBalance: "25.50"},
		Transaction: domain.Transaction{TransactionNo: "TX-CARD-1", Amount: "25.50"},
	}}
	delivery := &balanceWarningDeliveryStub{}
	uc := NewWalletUseCase(repo)
	uc.SetUserDirectory(balanceWarningDirectoryStub{users: map[uint]UserDirectoryEntry{
		7: {UserID: 7, Email: "user@example.com", Status: "active"},
	}})
	uc.SetMailDelivery(delivery)

	_, err := uc.RedeemCard(context.Background(), RedeemCardRequest{UserID: 7, CardKey: "CARD-1", IdempotencyKey: "idem-1"})
	require.NoError(t, err)
	require.Len(t, delivery.messages, 1)
	require.Contains(t, delivery.messages[0].TextBody, "TX-CARD-1")
	require.Contains(t, delivery.messages[0].TextBody, "25.50")

	repo.result.Replayed = true
	_, err = uc.RedeemCard(context.Background(), RedeemCardRequest{UserID: 7, CardKey: "CARD-1", IdempotencyKey: "idem-1"})
	require.NoError(t, err)
	require.Len(t, delivery.messages, 1)
}
