package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type pickupCredentialsRepoSpy struct {
	Repository
	orders map[string]domain.Order
	calls  int
}

func (r *pickupCredentialsRepoSpy) FindOrdersByOrderNos(_ context.Context, orderNos []string) (map[string]domain.Order, error) {
	r.calls++
	result := make(map[string]domain.Order, len(orderNos))
	for _, orderNo := range orderNos {
		if order, ok := r.orders[orderNo]; ok {
			result[orderNo] = order
		}
	}
	return result, nil
}

type pickupCredentialsTokenSpy struct {
	OrderTokenPort
	tokens map[string]OrderToken
	calls  int
	issues int
}

func (s *pickupCredentialsTokenSpy) FindOrderTokensByOrders(_ context.Context, orderNos []string) (map[string]OrderToken, error) {
	s.calls++
	result := make(map[string]OrderToken, len(orderNos))
	for _, orderNo := range orderNos {
		if token, ok := s.tokens[orderNo]; ok {
			result[orderNo] = token
		}
	}
	return result, nil
}

func (s *pickupCredentialsTokenSpy) IssueOrderToken(_ context.Context, _ string, _ *time.Time) (*OrderToken, error) {
	s.issues++
	return &OrderToken{TokenPlain: "generated"}, nil
}

func TestGetOrderPickupCredentialsUsesBatchReadersAndChecksOwnership(t *testing.T) {
	t.Run("batch", func(t *testing.T) {
		repo := &pickupCredentialsRepoSpy{orders: map[string]domain.Order{
			"OR1": {OrderNo: "OR1", UserID: 7, Status: domain.OrderStatusActive, DeliveryEmail: "one@example.com"},
			"OR2": {OrderNo: "OR2", UserID: 7, Status: domain.OrderStatusCompleted, DeliveryEmail: "two@example.com"},
		}}
		tokens := &pickupCredentialsTokenSpy{tokens: map[string]OrderToken{
			"OR1": {TokenPlain: "token-1"},
			"OR2": {TokenPlain: "token-2"},
		}}
		uc := NewUseCase(repo, nil, nil, nil, tokens)

		result, err := uc.GetOrderPickupCredentials(context.Background(), []string{"OR2", "OR1"}, 7)

		require.NoError(t, err)
		require.Equal(t, []OrderPickupCredential{
			{OrderNo: "OR2", DeliveryEmail: "two@example.com", ServiceToken: "token-2"},
			{OrderNo: "OR1", DeliveryEmail: "one@example.com", ServiceToken: "token-1"},
		}, result)
		require.Equal(t, 1, repo.calls)
		require.Equal(t, 1, tokens.calls)
	})

	t.Run("ownership", func(t *testing.T) {
		repo := &pickupCredentialsRepoSpy{orders: map[string]domain.Order{
			"OR1": {OrderNo: "OR1", UserID: 8, Status: domain.OrderStatusActive, DeliveryEmail: "other@example.com"},
		}}
		tokens := &pickupCredentialsTokenSpy{tokens: map[string]OrderToken{
			"OR1": {TokenPlain: "secret"},
		}}
		uc := NewUseCase(repo, nil, nil, nil, tokens)

		_, err := uc.GetOrderPickupCredentials(context.Background(), []string{"OR1"}, 7)

		require.ErrorIs(t, err, domain.ErrOrderForbidden)
		require.Equal(t, 1, repo.calls)
		require.Zero(t, tokens.calls)
	})

	t.Run("expired and disabled tokens", func(t *testing.T) {
		now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
		future := now.Add(time.Minute)
		repo := &pickupCredentialsRepoSpy{orders: map[string]domain.Order{
			"OR1": {OrderNo: "OR1", UserID: 7, ProductType: domain.ProductTypeGmail, Status: domain.OrderStatusActive, DeliveryEmail: "one@example.com"},
			"OR2": {OrderNo: "OR2", UserID: 7, ProductType: domain.ProductTypeGmail, Status: domain.OrderStatusCompleted, DeliveryEmail: "two@example.com"},
			"OR3": {OrderNo: "OR3", UserID: 7, Status: domain.OrderStatusActive, DeliveryEmail: "three@example.com"},
		}}
		tokens := &pickupCredentialsTokenSpy{tokens: map[string]OrderToken{
			"OR1": {TokenPlain: "expired", ExpireAt: &now},
			"OR3": {TokenPlain: "valid", ExpireAt: &future},
		}}
		uc := NewUseCase(repo, nil, nil, nil, tokens)
		uc.now = func() time.Time { return now }

		result, err := uc.GetOrderPickupCredentials(context.Background(), []string{"OR1", "OR2", "OR3"}, 7)

		require.NoError(t, err)
		require.Equal(t, []OrderPickupCredential{{
			OrderNo: "OR3", DeliveryEmail: "three@example.com", ServiceToken: "valid",
		}}, result)
		require.Zero(t, tokens.issues)
	})
}
