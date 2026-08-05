package app

import (
	"context"
	"errors"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/donnel666/remail/internal/upstream"
	"github.com/stretchr/testify/require"
)

type checkoutUpstreamSpy struct {
	quote       *upstream.SupplyQuote
	supplyCalls int
	acceptCalls int
	ownerCalls  int
	owned       bool
	acceptErr   error
	accepted    []upstream.PaidOrder
	acceptInTx  bool
	events      *[]string
}

func (s *checkoutUpstreamSpy) Supply(context.Context, upstream.Demand) (*upstream.SupplyQuote, error) {
	s.supplyCalls++
	return s.quote, nil
}

func (s *checkoutUpstreamSpy) AcceptPaidOrder(ctx context.Context, order upstream.PaidOrder) (bool, error) {
	s.acceptCalls++
	s.acceptInTx = ctx.Value(batchTxContextKey{}) != nil
	s.accepted = append(s.accepted, order)
	if s.events != nil {
		*s.events = append(*s.events, "accept_upstream")
	}
	return true, s.acceptErr
}

func (s *checkoutUpstreamSpy) OwnsOrder(context.Context, string) (bool, error) {
	s.ownerCalls++
	return s.owned, nil
}

func (*checkoutUpstreamSpy) CancelOrder(context.Context, string) (bool, error) {
	return false, nil
}

func (*checkoutUpstreamSpy) Pickup(context.Context, upstream.PickupRequest) (*upstream.PickupResult, bool, error) {
	return nil, false, nil
}

func TestUpstreamFirstGmailCheckoutPersistsProviderOwnerWithPayment(t *testing.T) {
	events := []string{}
	repo := &batchRepoSpy{orders: map[string]domain.Order{}, events: &events}
	wallet := &batchWalletSpy{events: &events}
	gmail := &checkoutGmailSupplySpy{}
	provider := &checkoutUpstreamSpy{
		quote:  &upstream.SupplyQuote{Strategy: upstream.StrategyUpstreamFirst, Available: 1},
		events: &events,
	}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-upstream", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusPaid, result.Order.Status)
	require.Equal(t, []string{"wallet_lock", "debit", "mark_paid", "accept_upstream"}, events)
	require.Equal(t, 1, wallet.debits)
	require.True(t, repo.paidInTx)
	require.Equal(t, 1, repo.committed)
	require.Zero(t, repo.rolledBack)
	require.Equal(t, 1, provider.acceptCalls)
	require.True(t, provider.acceptInTx)
	require.True(t, provider.accepted[0].Selected)
	require.Zero(t, gmail.creates)
	require.Zero(t, gmail.schedules)
}

func TestUpstreamOwnerFailureRollsBackPaymentTransaction(t *testing.T) {
	wantErr := errors.New("provider owner write failed")
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	gmail := &checkoutGmailSupplySpy{}
	provider := &checkoutUpstreamSpy{
		quote:     &upstream.SupplyQuote{Strategy: upstream.StrategyUpstreamFirst, Available: 1},
		acceptErr: wantErr,
	}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-upstream-rollback", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.Nil(t, result)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, wallet.debits)
	require.True(t, repo.paidInTx)
	require.Equal(t, 1, repo.rolledBack)
	require.Zero(t, repo.committed)
	require.True(t, provider.acceptInTx)
	require.Zero(t, gmail.creates)
}

func TestPaidProviderOrderResumesOwnerWithoutRecheckingSupply(t *testing.T) {
	order := domain.Order{
		ID: 1, OrderNo: "ORDER-UPSTREAM-PAID", UserID: 7, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeGmail, ServiceMode: domain.ServiceModeCode,
		SupplyPolicy: domain.SupplyPolicyPublicOnly, Status: domain.OrderStatusPaid,
		PayAmount: "1.00", CodeWindowMinutes: 1440, ClientChannel: domain.ClientChannelConsole,
		IdempotencyKey: "gmail-upstream-replay",
	}
	repo := &batchRepoSpy{orders: map[string]domain.Order{"gmail-upstream-replay": order}}
	wallet := &batchWalletSpy{}
	gmail := &checkoutGmailSupplySpy{}
	provider := &checkoutUpstreamSpy{owned: true}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-upstream-replay", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, order.OrderNo, result.Order.OrderNo)
	require.Zero(t, provider.supplyCalls)
	require.Equal(t, 1, provider.ownerCalls)
	require.Equal(t, 1, provider.acceptCalls)
	require.False(t, provider.accepted[0].Selected)
	require.Zero(t, gmail.checks)
	require.Zero(t, wallet.debits)
}

func TestLocalFirstGmailFallsBackUpstreamAfterFinalAllocationMiss(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	gmail := &checkoutGmailSupplySpy{allocationErr: domain.ErrInsufficientInventory}
	provider := &checkoutUpstreamSpy{quote: &upstream.SupplyQuote{Strategy: upstream.StrategyLocalFirst, Available: 1}}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-local-final-miss", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Created)
	require.Equal(t, domain.OrderStatusPaid, result.Order.Status)
	require.Equal(t, 1, gmail.allocations)
	require.Equal(t, 1, gmail.releases)
	require.Equal(t, 1, provider.acceptCalls)
	require.Equal(t, 1, wallet.debits)
}

func TestUpstreamFirstGmailFallsBackLocalAfterFinalReservationMiss(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	gmail := &checkoutGmailSupplySpy{}
	provider := &checkoutUpstreamSpy{
		quote:     &upstream.SupplyQuote{Strategy: upstream.StrategyUpstreamFirst, Available: 1},
		acceptErr: upstream.ErrUnavailable,
	}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-upstream-final-miss", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, domain.OrderStatusPaid, result.Order.Status)
	require.Equal(t, 1, provider.acceptCalls)
	require.Equal(t, 2, gmail.checks)
	require.Equal(t, 1, gmail.allocations)
	require.Equal(t, 1, gmail.creates)
	require.Equal(t, 1, gmail.schedules)
}
