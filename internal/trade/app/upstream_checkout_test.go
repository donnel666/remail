package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
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
	accept      func(context.Context, upstream.PaidOrder) error
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
	if s.acceptErr != nil {
		return true, s.acceptErr
	}
	if s.accept != nil {
		return true, s.accept(ctx, order)
	}
	return true, nil
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

func activateAcceptedOrder(repo *batchRepoSpy) func(context.Context, upstream.PaidOrder) error {
	return func(ctx context.Context, order upstream.PaidOrder) error {
		startedAt := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
		expiresAt := startedAt.Add(24 * time.Hour)
		_, err := repo.MarkActive(ctx, MarkActiveCommand{
			OrderNo: order.OrderNo, AllocationType: domain.AllocationTypeGmail,
			DeliveryEmail: "upstream@gmail.com", ReceiveStartedAt: startedAt,
			ReceiveUntil: expiresAt, AfterSaleUntil: &expiresAt,
		})
		return err
	}
}

func TestUpstreamFirstGmailCheckoutCommitsOnlyAfterActivation(t *testing.T) {
	events := []string{}
	repo := &batchRepoSpy{orders: map[string]domain.Order{}, events: &events}
	wallet := &batchWalletSpy{events: &events}
	gmail := &checkoutGmailSupplySpy{}
	provider := &checkoutUpstreamSpy{
		quote:  &upstream.SupplyQuote{Strategy: upstream.StrategyUpstreamFirst, Available: 1},
		events: &events,
		accept: activateAcceptedOrder(repo),
	}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-upstream", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "upstream@gmail.com", result.Order.DeliveryEmail)
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

func TestUpstreamCheckoutRollsBackWhenProviderDoesNotActivate(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	provider := &checkoutUpstreamSpy{
		quote: &upstream.SupplyQuote{Strategy: upstream.StrategyUpstreamFirst, Available: 1},
	}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, &batchWalletSpy{}, &checkoutInventorySpy{}, batchTokenSpy{})
	gmail := &checkoutGmailSupplySpy{}
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-upstream-not-active", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.Nil(t, result)
	require.ErrorContains(t, err, "without activating order")
	require.Equal(t, 1, repo.rolledBack)
	require.Zero(t, repo.committed)
}

func TestPaidProviderOrderResumesOwnerWithoutRecheckingSupply(t *testing.T) {
	order := domain.Order{
		ID: 1, OrderNo: "ORDER-UPSTREAM-PAID", UserID: 7, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeGmail, ServiceMode: domain.ServiceModeCode,
		SupplyPolicy: domain.SupplyPolicyPublicOnly, Status: domain.OrderStatusPaid,
		PayAmount: "1.00", CodeWindowMinutes: 10, ClientChannel: domain.ClientChannelConsole,
		IdempotencyKey: "gmail-upstream-replay",
	}
	repo := &batchRepoSpy{orders: map[string]domain.Order{"gmail-upstream-replay": order}}
	wallet := &batchWalletSpy{}
	gmail := &checkoutGmailSupplySpy{}
	provider := &checkoutUpstreamSpy{owned: true, accept: activateAcceptedOrder(repo)}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-upstream-replay", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, order.OrderNo, result.Order.OrderNo)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.Zero(t, provider.supplyCalls)
	require.Equal(t, 1, provider.ownerCalls)
	require.Equal(t, 1, provider.acceptCalls)
	require.False(t, provider.accepted[0].Selected)
	require.Zero(t, gmail.checks)
	require.Zero(t, wallet.debits)
}

func TestActivateUpstreamOrderUsesConfiguredReceiveWindowAndProviderWarranty(t *testing.T) {
	runtimeconfig.Set(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey, "7")
	t.Cleanup(func() { runtimeconfig.Delete(runtimeconfig.SMSBowerNoCodeRefundTimeoutMinutesKey) })
	startedAt := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	expiresAt := startedAt.Add(24 * time.Hour)
	order := domain.Order{
		ID: 1, OrderNo: "ORDER-UPSTREAM-ACTIVE", UserID: 7, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeGmail, ServiceMode: domain.ServiceModeCode,
		SupplyPolicy: domain.SupplyPolicyPublicOnly, Status: domain.OrderStatusPaid,
		CodeWindowMinutes: 10,
	}
	repo := &batchRepoSpy{orders: map[string]domain.Order{"upstream-active": order}}
	provider := &checkoutUpstreamSpy{owned: true}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, &batchWalletSpy{}, &checkoutInventorySpy{}, batchTokenSpy{})
	uc.SetUpstreams(upstream.NewRouter(provider))

	err := uc.ActivateUpstreamOrder(context.Background(), upstream.Activation{
		OrderNo: order.OrderNo, Email: "upstream@gmail.com", StartedAt: startedAt, ExpiresAt: expiresAt,
	})

	require.NoError(t, err)
	activated := repo.orders["upstream-active"]
	require.Equal(t, domain.OrderStatusActive, activated.Status)
	require.Equal(t, startedAt.Add(7*time.Minute), *activated.ReceiveUntil)
	require.Equal(t, expiresAt, *activated.AfterSaleUntil)
	receiveUntil, err := uc.GmailOrderReceiveUntil(context.Background(), order.OrderNo)
	require.NoError(t, err)
	require.Equal(t, startedAt.Add(7*time.Minute), receiveUntil)
}

func TestLocalFirstGmailFallsBackUpstreamAfterFinalAllocationMiss(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	gmail := &checkoutGmailSupplySpy{}
	allocation := &checkoutInventorySpy{allocationErr: domain.ErrInsufficientInventory}
	provider := &checkoutUpstreamSpy{
		quote:  &upstream.SupplyQuote{Strategy: upstream.StrategyLocalFirst, Available: 1},
		accept: activateAcceptedOrder(repo),
	}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, allocation, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-local-final-miss", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Created)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.Equal(t, 1, allocation.allocationCalls)
	require.Equal(t, 1, allocation.releaseCalls)
	require.Equal(t, 1, provider.acceptCalls)
	require.Equal(t, 1, wallet.debits)
}

func TestMainGmailLocalSupplyMissStillFallsBackUpstream(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	gmail := &checkoutGmailSupplySpy{checkErr: domain.ErrUpstreamUnavailable}
	allocation := &checkoutInventorySpy{}
	provider := &checkoutUpstreamSpy{
		quote:  &upstream.SupplyQuote{Strategy: upstream.StrategyLocalFirst, Available: 1},
		accept: activateAcceptedOrder(repo),
	}
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, allocation, batchTokenSpy{})
	uc.SetGmailPorts(gmail, gmail)
	uc.SetUpstreams(upstream.NewRouter(provider))
	request := batchRequest("gmail-local-supply-miss", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)

	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.Equal(t, 1, gmail.checks)
	require.Equal(t, 1, provider.supplyCalls)
	require.Equal(t, 1, provider.acceptCalls)
	require.Zero(t, allocation.allocationCalls)
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
	allocation := newCheckoutGmailInventorySpy()
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, allocation, batchTokenSpy{})
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
	require.Equal(t, 1, allocation.allocationCalls)
	require.Equal(t, 1, gmail.creates)
	require.Equal(t, 1, gmail.schedules)
}
