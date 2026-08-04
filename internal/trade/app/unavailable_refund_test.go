package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type unavailableRefundRepoStub struct {
	Repository
	resourceID       uint
	order            domain.Order
	refundCalls      int
	cleanupStatus    string
	unavailableLimit int
	checkoutRecovery bool
}

func (s *unavailableRefundRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *unavailableRefundRepoStub) FindOrder(context.Context, string) (*domain.Order, error) {
	order := s.order
	return &order, nil
}

func (s *unavailableRefundRepoStub) LockOrderForUpdate(context.Context, string) (*domain.Order, error) {
	order := s.order
	return &order, nil
}

func (s *unavailableRefundRepoStub) MarkFailed(_ context.Context, cmd MarkFailedCommand) (*domain.Order, error) {
	if s.order.Status == domain.OrderStatusPendingPayment {
		s.order.Status = domain.OrderStatusFailed
		s.order.FailureCode = cmd.FailureCode
	}
	order := s.order
	return &order, nil
}

func (s *unavailableRefundRepoStub) MarkActive(_ context.Context, cmd MarkActiveCommand) (*domain.Order, error) {
	if s.order.Status == domain.OrderStatusPaid {
		s.order.Status = domain.OrderStatusActive
		s.order.AllocationType = &cmd.AllocationType
		s.order.DeliveryEmail = cmd.DeliveryEmail
		s.order.ReceiveStartedAt = &cmd.ReceiveStartedAt
		s.order.ReceiveUntil = &cmd.ReceiveUntil
		s.order.ActivatedAt = cmd.ActivatedAt
		s.order.AfterSaleUntil = cmd.AfterSaleUntil
	}
	order := s.order
	return &order, nil
}

func (s *unavailableRefundRepoStub) RefundOrder(_ context.Context, cmd RefundOrderCommand) (*domain.Order, bool, error) {
	if s.order.Status == domain.OrderStatusRefunded {
		order := s.order
		return &order, false, nil
	}
	s.refundCalls++
	s.order.Status = domain.OrderStatusRefunded
	s.order.RefundTxID = &cmd.RefundTxID
	s.order.RefundAmount = cmd.RefundAmount
	order := s.order
	return &order, true, nil
}

func (s *unavailableRefundRepoStub) CompleteCodeOrder(_ context.Context, _ string, _ time.Time, _ time.Time) (*domain.Order, bool, error) {
	if s.order.ServiceMode != domain.ServiceModeCode || s.order.Status != domain.OrderStatusActive {
		order := s.order
		return &order, false, nil
	}
	s.order.Status = domain.OrderStatusCompleted
	order := s.order
	return &order, true, nil
}

func (s *unavailableRefundRepoStub) MarkServiceCleanup(_ context.Context, _ string, status string) error {
	s.cleanupStatus = status
	return nil
}

func (s *unavailableRefundRepoStub) ListUnavailableMicrosoftOrderNos(_ context.Context, resourceID uint, limit int) ([]string, error) {
	s.unavailableLimit = limit
	if s.order.ProductType == domain.ProductTypeGmail || s.order.Status != domain.OrderStatusActive || s.order.RefundTxID != nil || (resourceID != 0 && resourceID != s.resourceID) {
		return nil, nil
	}
	return []string{s.order.OrderNo}, nil
}

func (*unavailableRefundRepoStub) ListExpiredCodeOrderNos(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}

func (*unavailableRefundRepoStub) ListExpiredPurchaseActivationOrderNos(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}

func (*unavailableRefundRepoStub) ListExpiredPurchaseWarrantyOrderNos(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}

func (s *unavailableRefundRepoStub) ListCheckoutAllocationRecoveries(_ context.Context, before time.Time, _ int) ([]CheckoutAllocationRecovery, error) {
	if s.checkoutRecovery && s.order.CreatedAt.Before(before) {
		return []CheckoutAllocationRecovery{{
			OrderNo: s.order.OrderNo, Status: s.order.Status, ProductType: s.order.ProductType,
		}}, nil
	}
	return nil, nil
}

func (*unavailableRefundRepoStub) ListCodeOrderNosReadyForCleanup(context.Context, time.Time, int) ([]string, error) {
	return nil, nil
}

func (s *unavailableRefundRepoStub) ListPartialCleanupOrderNos(context.Context, int) ([]string, error) {
	if s.order.Status == domain.OrderStatusRefunded && s.cleanupStatus != "succeeded" {
		return []string{s.order.OrderNo}, nil
	}
	return nil, nil
}

type unavailableRefundWalletStub struct {
	WalletPort
	commands []WalletCommand
}

func (*unavailableRefundWalletStub) LockConsumer(context.Context, uint) error { return nil }

func (s *unavailableRefundWalletStub) RefundConsumer(_ context.Context, cmd WalletCommand) (*WalletTransaction, error) {
	s.commands = append(s.commands, cmd)
	return &WalletTransaction{ID: 9001}, nil
}

type unavailableRefundAllocationStub struct {
	AllocationPort
	released []string
}

func (s *unavailableRefundAllocationStub) ReleaseByOrder(_ context.Context, orderNo string) error {
	s.released = append(s.released, orderNo)
	return nil
}

type unavailableRefundTokenStub struct {
	OrderTokenPort
	disabled []string
	extended []string
}

func (s *unavailableRefundTokenStub) DisableOrderToken(_ context.Context, orderNo string, _ string) error {
	s.disabled = append(s.disabled, orderNo)
	return nil
}

func (s *unavailableRefundTokenStub) ExtendOrderToken(_ context.Context, orderNo string, _ time.Time) error {
	s.extended = append(s.extended, orderNo)
	return nil
}

type unavailableRefundDeliveryStub struct {
	OrderDeliveryPort
	delivery *OrderDeliverySummary
}

type unavailableRefundGmailStub struct {
	GmailSupplyPort
	cancelled []string
	released  []string
	err       error
}

func (s *unavailableRefundGmailStub) CancelGmailOrder(_ context.Context, orderNo string) error {
	s.cancelled = append(s.cancelled, orderNo)
	return s.err
}

func (s *unavailableRefundGmailStub) ReleaseLocalAllocation(_ context.Context, orderNo string) error {
	s.released = append(s.released, orderNo)
	return nil
}

func (s unavailableRefundDeliveryStub) FindOrderDelivery(context.Context, uint) (*OrderDeliverySummary, error) {
	return s.delivery, nil
}

func TestExpireDueOrdersReleasesStalePendingAllocation(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	repo := &unavailableRefundRepoStub{checkoutRecovery: true, order: domain.Order{
		OrderNo: "OR_STALE_PENDING", UserID: 42, ProductType: domain.ProductTypeMicrosoft,
		Status: domain.OrderStatusPendingPayment, CreatedAt: now.Add(-16 * time.Minute),
	}}
	allocation := &unavailableRefundAllocationStub{}
	uc := NewUseCase(repo, nil, &unavailableRefundWalletStub{}, allocation, &unavailableRefundTokenStub{})
	uc.now = func() time.Time { return now }

	result, err := uc.ExpireDueOrders(context.Background(), 200)

	require.NoError(t, err)
	require.Zero(t, result.Failed)
	require.Equal(t, domain.OrderStatusFailed, repo.order.Status)
	require.Equal(t, domain.OrderFailureAllocation, repo.order.FailureCode)
	require.Equal(t, []string{repo.order.OrderNo}, allocation.released)
}

func TestExpireDueOrdersReleasesStalePendingGmailAllocation(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	repo := &unavailableRefundRepoStub{checkoutRecovery: true, order: domain.Order{
		OrderNo: "OR_STALE_GMAIL", UserID: 42, ProductType: domain.ProductTypeGmail,
		Status: domain.OrderStatusPendingPayment, CreatedAt: now.Add(-16 * time.Minute),
	}}
	allocation := &unavailableRefundAllocationStub{}
	gmail := &unavailableRefundGmailStub{}
	uc := NewUseCase(repo, nil, nil, allocation, &unavailableRefundTokenStub{})
	uc.SetGmailPorts(gmail, nil)
	uc.now = func() time.Time { return now }

	result, err := uc.ExpireDueOrders(context.Background(), 200)

	require.NoError(t, err)
	require.Equal(t, 1, result.CheckoutRecovered)
	require.Zero(t, result.Failed)
	require.Equal(t, domain.OrderStatusFailed, repo.order.Status)
	require.Equal(t, domain.OrderFailureAllocation, repo.order.FailureCode)
	require.Equal(t, []string{repo.order.OrderNo}, gmail.released)
	require.Empty(t, allocation.released)
}

func TestExpireDueOrdersResumesPaidGmailPurchaseWithoutSupplyPrecheck(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	repo := &unavailableRefundRepoStub{checkoutRecovery: true, order: domain.Order{
		ID: 71, OrderNo: "OR_PAID_GMAIL", UserID: 42, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeGmail, ServiceMode: domain.ServiceModePurchase,
		SupplyPolicy: domain.SupplyPolicyPrivateFirst, Status: domain.OrderStatusPaid,
		PayAmount: "1.000000", ActivationWindowMinutes: 10, WarrantyMinutes: 10,
		CreatedAt: now.Add(-16 * time.Minute),
	}}
	supply := &checkoutGmailSupplySpy{purchase: &GmailPurchaseDelivery{
		AllocationID: 61, ResourceID: 51, SupplyScope: SupplyScopePublic,
		Email: "buyer@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop",
	}}
	tokens := &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
	uc := NewUseCase(repo, nil, nil, nil, tokens)
	uc.SetGmailPorts(supply, supply)
	uc.now = func() time.Time { return now }

	result, err := uc.ExpireDueOrders(context.Background(), 200)

	require.NoError(t, err)
	require.Equal(t, 1, result.CheckoutRecovered)
	require.Zero(t, result.Failed)
	require.Zero(t, supply.checks)
	require.Equal(t, 1, supply.purchases)
	require.Equal(t, domain.OrderStatusActive, repo.order.Status)
	require.Equal(t, "buyer@gmail.com", repo.order.DeliveryEmail)
	require.Equal(t, 1, tokens.issues)
}

func TestUnavailableMicrosoftResourceRefundIsImmediateAndIdempotent(t *testing.T) {
	debitTxID := uint(8001)
	repo := &unavailableRefundRepoStub{resourceID: 30043, order: domain.Order{
		OrderNo: "OR019F97158E9A713AA3A82FEB49DE0486", UserID: 42,
		Status: domain.OrderStatusActive, ServiceMode: domain.ServiceModePurchase,
		PayAmount: "0.010000", DebitTxID: &debitTxID,
	}}
	wallet := &unavailableRefundWalletStub{}
	allocation := &unavailableRefundAllocationStub{}
	tokens := &unavailableRefundTokenStub{}
	uc := NewUseCase(repo, nil, wallet, allocation, tokens)

	refunded, err := uc.RefundUnavailableMicrosoftOrders(context.Background(), 30043, repo.order.OrderNo)

	require.NoError(t, err)
	require.Equal(t, 1, refunded)
	require.Equal(t, 200, repo.unavailableLimit)
	require.Equal(t, domain.OrderStatusRefunded, repo.order.Status)
	require.Equal(t, "0.010000", repo.order.RefundAmount)
	require.NotNil(t, repo.order.RefundTxID)
	require.Equal(t, uint(9001), *repo.order.RefundTxID)
	require.Equal(t, "order:"+repo.order.OrderNo+":refund", wallet.commands[0].IdempotencyKey)
	require.Equal(t, []string{repo.order.OrderNo}, allocation.released)
	require.Equal(t, []string{repo.order.OrderNo}, tokens.disabled)
	require.Equal(t, "succeeded", repo.cleanupStatus)

	result, err := uc.ExpireDueOrders(context.Background(), 200)
	require.NoError(t, err)
	require.Zero(t, result.ResourceUnavailableRefunded)
	require.Equal(t, 1, repo.refundCalls)
	require.Len(t, wallet.commands, 1)
}

func TestUnavailableMicrosoftResourceCompletesDeliveredCodeOrderInsteadOfRefunding(t *testing.T) {
	debitTxID := uint(8002)
	repo := &unavailableRefundRepoStub{resourceID: 30044, order: domain.Order{
		ID: 77, OrderNo: "ORDER-DELIVERED-CODE", UserID: 42,
		Status: domain.OrderStatusActive, ServiceMode: domain.ServiceModeCode,
		PayAmount: "0.010000", DebitTxID: &debitTxID,
	}}
	wallet := &unavailableRefundWalletStub{}
	allocation := &unavailableRefundAllocationStub{}
	tokens := &unavailableRefundTokenStub{}
	uc := NewUseCase(repo, nil, wallet, allocation, tokens)
	uc.SetOrderDeliveryPort(unavailableRefundDeliveryStub{delivery: &OrderDeliverySummary{ReceivedAt: time.Now().UTC()}})

	refunded, err := uc.RefundUnavailableMicrosoftOrders(context.Background(), repo.resourceID, "request-delivered")

	require.NoError(t, err)
	require.Zero(t, refunded)
	require.Equal(t, domain.OrderStatusCompleted, repo.order.Status)
	require.Zero(t, repo.refundCalls)
	require.Empty(t, wallet.commands)
	require.Empty(t, allocation.released)
	require.Equal(t, []string{repo.order.OrderNo}, tokens.extended)
	require.Empty(t, tokens.disabled)
}

func TestRefundedOrderWithMissingCleanupIsRecovered(t *testing.T) {
	debitTxID := uint(8003)
	refundTxID := uint(9003)
	repo := &unavailableRefundRepoStub{resourceID: 30045, cleanupStatus: "none", order: domain.Order{
		OrderNo: "ORDER-CLEANUP-RECOVERY", UserID: 42,
		Status: domain.OrderStatusRefunded, ServiceMode: domain.ServiceModePurchase,
		PayAmount: "0.010000", DebitTxID: &debitTxID, RefundTxID: &refundTxID,
	}}
	allocation := &unavailableRefundAllocationStub{}
	tokens := &unavailableRefundTokenStub{}
	uc := NewUseCase(repo, nil, nil, allocation, tokens)

	result, err := uc.ExpireDueOrders(context.Background(), 200)

	require.NoError(t, err)
	require.Equal(t, 1, result.CleanupRetried)
	require.Equal(t, []string{repo.order.OrderNo}, allocation.released)
	require.Equal(t, []string{repo.order.OrderNo}, tokens.disabled)
	require.Equal(t, "succeeded", repo.cleanupStatus)
}

func TestGmailCleanupCancelsUpstreamAndRecordsFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		cancelErr  error
		wantStatus string
		wantErr    bool
	}{
		{name: "success", wantStatus: "succeeded"},
		{name: "cancel failure", cancelErr: errors.New("upstream unavailable"), wantStatus: "partial_failure", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &unavailableRefundRepoStub{order: domain.Order{OrderNo: "GMAIL-CLEANUP", ProductType: domain.ProductTypeGmail}}
			allocation := &unavailableRefundAllocationStub{}
			tokens := &unavailableRefundTokenStub{}
			gmail := &unavailableRefundGmailStub{err: test.cancelErr}
			uc := NewUseCase(repo, nil, nil, allocation, tokens)
			uc.SetGmailPorts(gmail, nil)

			err := uc.cleanupOrderService(context.Background(), repo.order, true, "Order refunded.", "request-1")
			if test.wantErr {
				require.ErrorIs(t, err, domain.ErrOrderCompensationError)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []string{repo.order.OrderNo}, gmail.cancelled)
			require.Equal(t, []string{repo.order.OrderNo}, gmail.released)
			require.Empty(t, allocation.released)
			require.Equal(t, []string{repo.order.OrderNo}, tokens.disabled)
			require.Equal(t, test.wantStatus, repo.cleanupStatus)
		})
	}
}
