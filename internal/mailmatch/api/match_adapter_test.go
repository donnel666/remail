package api

import (
	"context"
	"testing"
	"time"

	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type purchaseMatchRepoStub struct {
	tradeapp.Repository
	order     tradedomain.Order
	activated int
	completed int
}

func (s *purchaseMatchRepoStub) FindOrder(context.Context, string) (*tradedomain.Order, error) {
	order := s.order
	return &order, nil
}

func (*purchaseMatchRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *purchaseMatchRepoStub) ActivatePurchaseOrder(_ context.Context, _ string, matchedAt, afterSaleUntil time.Time) (*tradedomain.Order, bool, error) {
	s.activated++
	s.order.ActivatedAt = &matchedAt
	s.order.AfterSaleUntil = &afterSaleUntil
	return &s.order, true, nil
}

func (s *purchaseMatchRepoStub) CompleteCodeOrder(_ context.Context, _ string, _ time.Time, readUntil time.Time) (*tradedomain.Order, bool, error) {
	s.completed++
	s.order.Status = tradedomain.OrderStatusCompleted
	s.order.ReceiveUntil = &readUntil
	return &s.order, true, nil
}

type purchaseMatchTokenStub struct {
	tradeapp.OrderTokenPort
	extended int
}

func (s *purchaseMatchTokenStub) ExtendOrderToken(context.Context, string, time.Time) error {
	s.extended++
	return nil
}

func TestMatchResultAdapterRoutesGmailCodeToSharedTradeLifecycle(t *testing.T) {
	matchedAt := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	repo := &purchaseMatchRepoStub{order: tradedomain.Order{
		OrderNo: "OR_GMAIL_MATCH", ServiceMode: tradedomain.ServiceModeCode,
		Status: tradedomain.OrderStatusActive,
	}}
	tokens := &purchaseMatchTokenStub{}
	trade := tradeapp.NewUseCase(repo, nil, nil, nil, tokens)
	adapter := &matchResultAdapter{trade: trade}

	err := adapter.NotifyMatchedCode(context.Background(), mailmatchapp.MatchResult{
		OrderNo: "OR_GMAIL_EMPTY", ResourceType: domain.ResourceTypeGmail,
		ServiceMode: "code", VerificationCode: "  ", MatchedAt: matchedAt,
	})
	require.NoError(t, err)
	require.Zero(t, repo.completed)

	err = adapter.NotifyMatchedCode(context.Background(), mailmatchapp.MatchResult{
		OrderNo:          "OR_GMAIL_MATCH",
		ResourceType:     domain.ResourceTypeGmail,
		ServiceMode:      "code",
		VerificationCode: "123456",
		MatchedAt:        matchedAt,
	})

	require.NoError(t, err)
	require.Equal(t, 1, repo.completed)
	require.Equal(t, 1, tokens.extended)
}

func TestMatchResultAdapterRoutesGmailPurchaseToSharedTradeLifecycle(t *testing.T) {
	matchedAt := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	receiveUntil := matchedAt.Add(time.Hour)
	repo := &purchaseMatchRepoStub{order: tradedomain.Order{
		OrderNo: "OR_GMAIL_PURCHASE", ProjectID: 1, ProjectProductID: 2, ProductType: tradedomain.ProductTypeGmail,
		ServiceMode: tradedomain.ServiceModePurchase, Status: tradedomain.OrderStatusActive,
		ReceiveUntil: &receiveUntil, ActivationWindowMinutes: 60, WarrantyMinutes: 60,
	}}
	trade := tradeapp.NewUseCase(repo, nil, nil, nil, &purchaseMatchTokenStub{})
	adapter := &matchResultAdapter{trade: trade}

	err := adapter.NotifyMatchedCode(context.Background(), mailmatchapp.MatchResult{
		OrderNo: "OR_GMAIL_PURCHASE", ResourceType: domain.ResourceTypeGmail,
		ServiceMode: "purchase", MatchedAt: matchedAt,
	})

	require.NoError(t, err)
	require.Equal(t, 1, repo.activated)
	require.NotNil(t, repo.order.ActivatedAt)
}
