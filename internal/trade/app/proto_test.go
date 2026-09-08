package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type protoRefundRepo struct{ *unavailableRefundRepoStub }

func (r protoRefundRepo) ListUnavailableProtoOrderNos(_ context.Context, resourceID uint, _ int) ([]string, error) {
	if r.order.Status != domain.OrderStatusActive || resourceID != r.resourceID {
		return nil, nil
	}
	return []string{r.order.OrderNo}, nil
}

func TestProtoPermanentFailureUsesUnifiedRefundAndCleanup(t *testing.T) {
	allocationType := domain.AllocationTypeProto
	repo := protoRefundRepo{&unavailableRefundRepoStub{resourceID: 91, order: domain.Order{
		ID: 8, OrderNo: "PROTO-1", UserID: 2, ProductType: domain.ProductTypeProto,
		ServiceMode: domain.ServiceModeCode, Status: domain.OrderStatusActive, PayAmount: "3.00", AllocationType: &allocationType,
	}}}
	wallet, allocations, tokens := &unavailableRefundWalletStub{}, &unavailableRefundAllocationStub{}, &unavailableRefundTokenStub{}
	uc := NewUseCase(repo, nil, wallet, allocations, tokens)
	count, err := uc.RefundUnavailableProtoOrders(context.Background(), 91, "proto-failure")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, domain.OrderStatusRefunded, repo.order.Status)
	require.Equal(t, "succeeded", repo.cleanupStatus)
	require.Equal(t, []string{"PROTO-1"}, allocations.released)
	require.Equal(t, []string{"PROTO-1"}, tokens.disabled)
	require.Len(t, wallet.commands, 1)
	count, err = uc.RefundUnavailableProtoOrders(context.Background(), 91, "proto-failure")
	require.NoError(t, err)
	require.Zero(t, count)
	require.Len(t, wallet.commands, 1)
}

type protoHistoryRepo struct{ *historicalImportRepoSpy }

func (r protoHistoryRepo) CreateHistoricalProtoOrder(context.Context, CreateHistoricalProtoOrderCommand) error {
	*r.events = append(*r.events, "proto-order")
	return nil
}

type protoHistoryAllocation struct{ *historicalImportAllocationSpy }

func (a protoHistoryAllocation) ImportHistoricalProtoAllocation(context.Context, HistoricalProtoAllocationCommand) (*AllocationResult, error) {
	*a.events = append(*a.events, "proto-allocation")
	return a.result, nil
}

func TestProtoHistoricalUsageRequiresEvidenceAndAtomicFacts(t *testing.T) {
	events := []string{}
	repo := protoHistoryRepo{&historicalImportRepoSpy{events: &events}}
	allocations := protoHistoryAllocation{&historicalImportAllocationSpy{events: &events, result: &AllocationResult{
		OrderNo: "HIST-PROTO-91-10", Type: domain.AllocationTypeProto, ID: 1,
		ProductID: 20, SupplyScope: SupplyScopePublic, Email: "one@example.com", Created: true,
	}}}
	uc := NewUseCase(repo, nil, &historicalImportWalletSpy{events: &events}, allocations, nil)
	match := HistoricalProtoUsage{ResourceID: 91, ProjectID: 10, ProductID: 20, ProductType: domain.ProductTypeProto,
		Mailbox: "main", Email: "one@example.com", FirstMatchedAt: time.Now().Add(-time.Hour), LastMatchedAt: time.Now()}
	require.ErrorIs(t, uc.ImportHistoricalProtoUsage(context.Background(), []HistoricalProtoUsage{match}), domain.ErrInvalidOrderRequest)
	require.Empty(t, events)
	match.EvidenceCount = 1
	require.NoError(t, uc.ImportHistoricalProtoUsage(context.Background(), []HistoricalProtoUsage{match}))
	require.Equal(t, []string{"proto-allocation", "find-order", "zero-debit", "proto-order"}, events)
	events = nil
	allocations.result = nil
	require.NoError(t, uc.ImportHistoricalProtoUsage(context.Background(), []HistoricalProtoUsage{match}))
	require.Equal(t, []string{"proto-allocation"}, events)
}

type protoRecoveryAllocation struct {
	*unavailableRefundAllocationStub
	ready             bool
	stopAfterAllocate bool
	missingSession    bool
	loseSession       bool
	readinessErr      error
	calls             int
}

func (a *protoRecoveryAllocation) ProtoProtocolReady() bool { return a.ready }

func (a *protoRecoveryAllocation) ProtoAllocationReady(context.Context, string, uint) (bool, error) {
	return !a.missingSession && a.readinessErr == nil, a.readinessErr
}

func (a *protoRecoveryAllocation) Allocate(ctx context.Context, cmd AllocationCommand) (*AllocationResult, error) {
	a.calls++
	if a.stopAfterAllocate {
		a.ready = false
	}
	if a.loseSession {
		a.missingSession = true
	}
	return a.unavailableRefundAllocationStub.Allocate(ctx, cmd)
}

func TestProtoPaidRecoveryRequiresProtocolButActiveReplayDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		ready, stopAfterAllocate, missingCapability bool
	}{
		{name: "unconfigured"},
		{name: "legacy-adapter", missingCapability: true},
		{name: "closed-during-resolution", ready: true, stopAfterAllocate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &unavailableRefundRepoStub{order: domain.Order{
				ID: 71, OrderNo: "PROTO-OLD-PAID", UserID: 42, ProjectID: 8, ProjectProductID: 9,
				ProductType: domain.ProductTypeProto, ServiceMode: domain.ServiceModePurchase,
				Status: domain.OrderStatusPaid, PayAmount: "1.00", ActivationWindowMinutes: 10, WarrantyMinutes: 10,
			}}
			baseAllocation := &unavailableRefundAllocationStub{allocation: &AllocationResult{
				Type: domain.AllocationTypeProto, ID: 61, ProductID: 9, Email: "one@example.com", SupplyScope: SupplyScopePublic,
			}}
			readyAllocation := &protoRecoveryAllocation{unavailableRefundAllocationStub: baseAllocation,
				ready: tc.ready, stopAfterAllocate: tc.stopAfterAllocate}
			var allocations AllocationPort = readyAllocation
			if tc.missingCapability {
				allocations = baseAllocation
			}
			tokens := &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
			uc := NewUseCase(repo, nil, nil, allocations, tokens)

			result, err := uc.resumeExistingCheckout(context.Background(), repo.order.OrderNo, "", "")
			require.ErrorIs(t, err, domain.ErrProjectUnavailable)
			require.NotNil(t, result)
			require.Equal(t, domain.OrderStatusPaid, result.Order.Status)
			require.Equal(t, domain.OrderStatusPaid, repo.order.Status)
			require.Empty(t, result.ServiceToken)
			require.Zero(t, tokens.issues)
			require.Empty(t, baseAllocation.released)

			tokens.tokens[repo.order.OrderNo] = &OrderToken{TokenPlain: "existing-token"}
			for _, status := range []domain.OrderStatus{domain.OrderStatusActive, domain.OrderStatusCompleted} {
				repo.order.Status = status
				result, err = uc.resumeExistingCheckout(context.Background(), repo.order.OrderNo, "", "")
				require.NoError(t, err)
				require.Equal(t, "existing-token", result.ServiceToken)
				require.Equal(t, status, result.Order.Status)
				require.Zero(t, tokens.issues)
			}
		})
	}
}

func TestProtoPaidReadinessDatabaseErrorDoesNotActivateOrRefund(t *testing.T) {
	repo := &unavailableRefundRepoStub{order: domain.Order{
		ID: 71, OrderNo: "PROTO-PAID-DB-FAILURE", UserID: 42, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeProto, ServiceMode: domain.ServiceModePurchase,
		Status: domain.OrderStatusPaid, PayAmount: "1.00", ActivationWindowMinutes: 10, WarrantyMinutes: 10,
	}}
	failure := errors.New("readiness query failed")
	allocations := &protoRecoveryAllocation{unavailableRefundAllocationStub: &unavailableRefundAllocationStub{}, ready: true, readinessErr: failure}
	tokens := &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
	uc := NewUseCase(repo, nil, nil, allocations, tokens)
	_, err := uc.resumeExistingCheckout(context.Background(), repo.order.OrderNo, "", "")
	require.ErrorIs(t, err, failure)
	require.Equal(t, domain.OrderStatusPaid, repo.order.Status)
	require.Zero(t, tokens.issues)
	require.Empty(t, allocations.released)
}

type protoPaidCompensationRepo struct{ *unavailableRefundRepoStub }

func (r *protoPaidCompensationRepo) MarkFailed(ctx context.Context, cmd MarkFailedCommand) (*domain.Order, error) {
	if r.order.Status != domain.OrderStatusPaid || cmd.RefundTxID == nil {
		return r.unavailableRefundRepoStub.MarkFailed(ctx, cmd)
	}
	r.order.Status, r.order.FailureCode = domain.OrderStatusFailed, cmd.FailureCode
	r.order.RefundTxID, r.order.RefundAmount = cmd.RefundTxID, cmd.RefundAmount
	r.checkoutRecovery = false
	snapshot := r.order
	return &snapshot, nil
}

type protoPaidActivationRaceRepo struct{ *protoPaidCompensationRepo }

func (r *protoPaidActivationRaceRepo) LockOrderForUpdate(context.Context, string) (*domain.Order, error) {
	r.order.Status = domain.OrderStatusActive
	snapshot := r.order
	return &snapshot, nil
}

type protoPaidTokenSpy struct {
	*issuedOrderTokenSpy
	disabled []string
}

func (s *protoPaidTokenSpy) DisableOrderToken(_ context.Context, orderNo, _ string) error {
	s.disabled = append(s.disabled, orderNo)
	return nil
}

func TestProtoPaidWithoutUsableSessionRefundsOnlyThatUnfulfilledOrder(t *testing.T) {
	for _, loseAfterAllocation := range []bool{false, true} {
		t.Run(fmt.Sprint(loseAfterAllocation), func(t *testing.T) {
			debitID := uint(10)
			repo := &protoPaidCompensationRepo{&unavailableRefundRepoStub{checkoutRecovery: true, order: domain.Order{
				ID: 71, OrderNo: "PROTO-UNFULFILLED", UserID: 42, ProjectID: 8, ProjectProductID: 9,
				ProductType: domain.ProductTypeProto, ServiceMode: domain.ServiceModePurchase, DebitTxID: &debitID,
				Status: domain.OrderStatusPaid, PayAmount: "1.00", ActivationWindowMinutes: 10, WarrantyMinutes: 10,
				CreatedAt: time.Now().UTC().Add(-time.Hour),
			}}}
			allocations := &protoRecoveryAllocation{unavailableRefundAllocationStub: &unavailableRefundAllocationStub{allocation: &AllocationResult{
				Type: domain.AllocationTypeProto, ID: 61, ProductID: 9, Email: "one@proton.me", SupplyScope: SupplyScopePublic,
			}}, ready: true, missingSession: !loseAfterAllocation, loseSession: loseAfterAllocation}
			tokens := &protoPaidTokenSpy{issuedOrderTokenSpy: &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}}
			wallet := &unavailableRefundWalletStub{}
			uc := NewUseCase(repo, nil, wallet, allocations, tokens)
			ctx := context.Background()
			_, err := uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Equal(t, domain.OrderStatusFailed, repo.order.Status)
			require.Equal(t, domain.OrderFailureInsufficientInventory, repo.order.FailureCode)
			require.NotNil(t, repo.order.RefundTxID)
			require.Equal(t, repo.order.PayAmount, repo.order.RefundAmount)
			require.Len(t, wallet.commands, 1)
			require.Equal(t, "order:"+repo.order.OrderNo+":refund", wallet.commands[0].IdempotencyKey)
			require.Equal(t, []string{repo.order.OrderNo}, allocations.released)
			require.Zero(t, tokens.issues)
			_, err = uc.ExpireDueOrders(ctx, 200)
			require.NoError(t, err)
			require.Len(t, wallet.commands, 1, "the compensated paid order must leave the recovery queue")
			tokens.tokens[repo.order.OrderNo] = &OrderToken{TokenPlain: "existing-token"}
			for _, status := range []domain.OrderStatus{domain.OrderStatusActive, domain.OrderStatusCompleted} {
				repo.order.Status = status
				result, err := uc.resumeExistingCheckout(ctx, repo.order.OrderNo, "", "")
				require.NoError(t, err)
				require.Equal(t, status, result.Order.Status)
				require.Equal(t, "existing-token", result.ServiceToken)
			}
			require.Len(t, wallet.commands, 1)
		})
	}
}

func TestProtoPaidConcurrentActivationKeepsExistingTokenWithoutRefund(t *testing.T) {
	repo := &protoPaidActivationRaceRepo{&protoPaidCompensationRepo{&unavailableRefundRepoStub{order: domain.Order{
		ID: 72, OrderNo: "PROTO-CONCURRENT-ACTIVATION", UserID: 42, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeProto, ServiceMode: domain.ServiceModePurchase,
		Status: domain.OrderStatusPaid, PayAmount: "1.00", ActivationWindowMinutes: 10, WarrantyMinutes: 10,
	}}}}
	allocations := &protoRecoveryAllocation{unavailableRefundAllocationStub: &unavailableRefundAllocationStub{}, ready: true, missingSession: true}
	tokens := &protoPaidTokenSpy{issuedOrderTokenSpy: &issuedOrderTokenSpy{tokens: map[string]*OrderToken{repo.order.OrderNo: {TokenPlain: "already-active-token"}}}}
	wallet := &unavailableRefundWalletStub{}
	uc := NewUseCase(repo, nil, wallet, allocations, tokens)
	result, err := uc.resumeExistingCheckout(context.Background(), repo.order.OrderNo, "", "")
	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "already-active-token", result.ServiceToken)
	require.Empty(t, wallet.commands)
	require.Empty(t, allocations.released)
	require.Empty(t, tokens.disabled)
	require.Zero(t, tokens.issues)
}

type protoFilteredRecoveryRepo struct {
	*unavailableRefundRepoStub
	filteredCalls int
	ordinaryCalls int
}

func (r *protoFilteredRecoveryRepo) ListCheckoutAllocationRecoveries(ctx context.Context, before time.Time, limit int) ([]CheckoutAllocationRecovery, error) {
	r.ordinaryCalls++
	return r.unavailableRefundRepoStub.ListCheckoutAllocationRecoveries(ctx, before, limit)
}

func (r *protoFilteredRecoveryRepo) ListCheckoutAllocationRecoveriesExcludingProtoPaid(ctx context.Context, before time.Time, limit int) ([]CheckoutAllocationRecovery, error) {
	r.filteredCalls++
	return r.unavailableRefundRepoStub.ListCheckoutAllocationRecoveries(ctx, before, limit)
}

func TestProtoPausedPaymentsUseFilteredRecoveryQuery(t *testing.T) {
	for _, tc := range []struct {
		name              string
		ready, capability bool
	}{
		{name: "paused", capability: true},
		{name: "ready", ready: true, capability: true},
		{name: "no-protocol-capability"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &protoFilteredRecoveryRepo{unavailableRefundRepoStub: &unavailableRefundRepoStub{
				checkoutRecovery: true, order: domain.Order{
					OrderNo: "OLD-MICROSOFT", UserID: 2, ProductType: domain.ProductTypeMicrosoft,
					Status: domain.OrderStatusPendingPayment, CreatedAt: time.Now().UTC().Add(-time.Hour),
				},
			}}
			base := &unavailableRefundAllocationStub{}
			var allocations AllocationPort = base
			if tc.capability {
				allocations = &protoRecoveryAllocation{unavailableRefundAllocationStub: base, ready: tc.ready}
			}
			uc := NewUseCase(repo, nil, nil, allocations, &unavailableRefundTokenStub{})
			result, err := uc.ExpireDueOrders(context.Background(), 200)
			require.NoError(t, err)
			require.Zero(t, result.Failed)
			require.Equal(t, 1, result.CheckoutRecovered)
			require.Equal(t, []string{"OLD-MICROSOFT"}, base.released)
			if tc.ready {
				require.Equal(t, 1, repo.ordinaryCalls)
				require.Zero(t, repo.filteredCalls)
			} else {
				require.Zero(t, repo.ordinaryCalls)
				require.Equal(t, 1, repo.filteredCalls)
			}
		})
	}
}
