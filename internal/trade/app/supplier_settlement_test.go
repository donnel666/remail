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

type supplierOwnerStub struct {
	AllocationPort
	owner uint
}

func (s supplierOwnerStub) AllocationSupplierUserID(context.Context, string) (uint, error) {
	return s.owner, nil
}

func TestSelfPurchaseHasNoDebitOrSupplierIncome(t *testing.T) {
	uc := &UseCase{allocation: supplierOwnerStub{owner: 7}}
	for _, supplierAmount := range []string{"48.00", "0.00"} {
		pay, supplier, owner, err := uc.checkoutAmounts(context.Background(), domain.Order{UserID: 7, SupplierAmount: supplierAmount}, AllocationResult{SupplyScope: SupplyScopePublic}, "80.00")
		require.NoError(t, err)
		require.Equal(t, "0.00", pay)
		require.Equal(t, "0.00", supplier)
		require.Nil(t, owner)
	}
	owner := uint(7)
	_, err := uc.activateAndFreezeSupplier(context.Background(), domain.Order{UserID: 7, SupplierUserID: &owner, SupplierAmount: "48.00"}, MarkActiveCommand{})
	require.ErrorIs(t, err, domain.ErrInvalidOrderRequest)
}

type supplierScanRepoStub struct {
	Repository
	t             *testing.T
	orders        []SupplierSettlementOrder
	queries       int
	failing, held string
	blocked       string
}

func (r *supplierScanRepoStub) WithSupplierSettlementScan(_ context.Context, fn func() error) error {
	return fn()
}

func (r *supplierScanRepoStub) ListDueSupplierOrders(ctx context.Context, _ time.Time, after *SupplierSettlementOrder, limit int) ([]SupplierSettlementOrder, error) {
	r.queries++
	require.LessOrEqual(r.t, r.queries, 4, "scan must advance instead of repeating its failed first page")
	deadline, ok := ctx.Deadline()
	require.True(r.t, ok)
	require.LessOrEqual(r.t, time.Until(deadline), supplierSettlementScanBudget)
	start := 0
	if after != nil {
		for start < len(r.orders) && r.orders[start].OrderNo <= after.OrderNo {
			start++
		}
	}
	return r.orders[start:min(start+limit, len(r.orders))], nil
}
func (r *supplierScanRepoStub) FindOrder(ctx context.Context, orderNo string) (*domain.Order, error) {
	if orderNo == r.blocked {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if orderNo == r.failing {
		return nil, errors.New("temporary order failure")
	}
	owner := uint(1)
	due := time.Now().Add(-time.Hour)
	return &domain.Order{OrderNo: orderNo, UserID: 2, SupplierUserID: &owner, SupplierAmount: "1.00", Status: domain.OrderStatusCompleted, ServiceMode: domain.ServiceModeCode, ReceiveUntil: &due}, nil
}
func (r *supplierScanRepoStub) LockOrderForUpdate(ctx context.Context, orderNo string) (*domain.Order, error) {
	return r.FindOrder(ctx, orderNo)
}
func (r *supplierScanRepoStub) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *supplierScanRepoStub) HasOpenOrderTicket(_ context.Context, orderNo string) (bool, error) {
	return orderNo == r.held, nil
}

type supplierScanWalletStub struct {
	SupplierSettlementPort
	credited    []string
	afterCredit func()
}

func (w *supplierScanWalletStub) LockSupplierWallets(context.Context, ...uint) error { return nil }
func (w *supplierScanWalletStub) CreditSupplierSettlement(_ context.Context, orderNo string, _ time.Time) (bool, error) {
	for _, credited := range w.credited {
		if credited == orderNo {
			return false, nil
		}
	}
	w.credited = append(w.credited, orderNo)
	if w.afterCredit != nil {
		w.afterCredit()
	}
	return true, nil
}

func TestSupplierScanDrainsMultiplePagesPastFailuresAndNewDisputes(t *testing.T) {
	repo := &supplierScanRepoStub{t: t, failing: "OR-000", held: "OR-001"}
	for i := range 450 {
		repo.orders = append(repo.orders, SupplierSettlementOrder{OrderNo: fmt.Sprintf("OR-%03d", i), AvailableAt: time.Now().Add(-time.Hour)})
	}
	wallet := &supplierScanWalletStub{}
	uc := &UseCase{repo: repo, settlements: wallet}
	result := &ExpireOrdersResult{}
	require.NoError(t, uc.settleDueSuppliers(context.Background(), time.Now(), 200, result))
	require.Equal(t, 3, repo.queries)
	require.Equal(t, 448, result.SupplierSettled)
	require.Equal(t, 1, result.Failed)
	require.Len(t, wallet.credited, 448)
	require.Equal(t, "OR-449", wallet.credited[len(wallet.credited)-1])
}

func TestSupplierScanStopsOnCancellationWithCommittedProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &supplierScanRepoStub{t: t, orders: []SupplierSettlementOrder{{OrderNo: "OR-001"}, {OrderNo: "OR-002"}}}
	wallet := &supplierScanWalletStub{afterCredit: cancel}
	uc := &UseCase{repo: repo, settlements: wallet}
	result := &ExpireOrdersResult{}
	require.ErrorIs(t, uc.settleDueSuppliers(ctx, time.Now(), 200, result), context.Canceled)
	require.Equal(t, 1, result.SupplierSettled)
	require.Zero(t, result.Failed)
	require.Equal(t, []string{"OR-001"}, wallet.credited)
}

func TestSupplierScanDeadlineIsReportedAndCursorResumesPastBlockedOrder(t *testing.T) {
	repo := &supplierScanRepoStub{t: t, blocked: "OR-001", orders: []SupplierSettlementOrder{{OrderNo: "OR-001"}, {OrderNo: "OR-002"}}}
	wallet := &supplierScanWalletStub{}
	uc := &UseCase{repo: repo, settlements: wallet}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	first := &ExpireOrdersResult{}
	require.ErrorIs(t, uc.scanDueSuppliers(ctx, repo, time.Now(), 200, first), context.DeadlineExceeded)
	require.Equal(t, 1, first.Failed)
	require.Equal(t, "OR-001", uc.supplierScanAfter.OrderNo)

	second := &ExpireOrdersResult{}
	require.NoError(t, uc.settleDueSuppliers(context.Background(), time.Now(), 200, second))
	require.Equal(t, 1, second.SupplierSettled)
	require.Zero(t, second.Failed)
	require.Equal(t, []string{"OR-002"}, wallet.credited)
	require.Nil(t, uc.supplierScanAfter)

	repo.blocked = ""
	third := &ExpireOrdersResult{}
	require.NoError(t, uc.settleDueSuppliers(context.Background(), time.Now(), 200, third))
	require.Equal(t, 1, third.SupplierSettled, "an idempotent credit is not another settlement")
	require.Equal(t, []string{"OR-002", "OR-001"}, wallet.credited)
}

func TestSupplierSettlementDuePreservesWarrantyAndReadWindows(t *testing.T) {
	start := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	receive := start.Add(time.Hour)
	warranty := start.Add(24 * time.Hour)
	for _, tt := range []struct {
		name     string
		order    domain.Order
		expected time.Time
	}{
		{"code read window", domain.Order{ServiceMode: domain.ServiceModeCode, ReceiveUntil: &receive}, receive},
		{"activated purchase", domain.Order{ServiceMode: domain.ServiceModePurchase, ReceiveUntil: &receive, AfterSaleUntil: &warranty}, warranty},
		{"unactivated purchase retains possible warranty", domain.Order{ServiceMode: domain.ServiceModePurchase, ReceiveStartedAt: &start, ReceiveUntil: &receive, WarrantyMinutes: 1440}, warranty},
		{"activation window longer than warranty", domain.Order{ServiceMode: domain.ServiceModePurchase, ReceiveStartedAt: &start, ReceiveUntil: &warranty, WarrantyMinutes: 60}, warranty},
	} {
		t.Run(tt.name, func(t *testing.T) {
			due, err := supplierSettlementDue(tt.order)
			require.NoError(t, err)
			require.Equal(t, tt.expected, due)
		})
	}
	_, err := supplierSettlementDue(domain.Order{})
	require.ErrorIs(t, err, domain.ErrInvalidOrderRequest)
}

func TestSupplierCheckoutExcludesLegacyOwnedAndZeroIncome(t *testing.T) {
	uc := &UseCase{}
	for _, tt := range []struct {
		name, amount string
		scope        SupplyScope
		expected     string
	}{
		{"legacy", "", SupplyScopePublic, ""},
		{"owned", "48.00", SupplyScopeOwned, "0.00"},
		{"zero discount", "0.00", SupplyScopePublic, "0.00"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, amount, owner, err := uc.checkoutAmounts(context.Background(), domain.Order{SupplierAmount: tt.amount}, AllocationResult{SupplyScope: tt.scope}, "0.00")
			require.NoError(t, err)
			require.Equal(t, tt.expected, amount)
			require.Nil(t, owner)
		})
	}
	_, _, _, err := uc.checkoutAmounts(context.Background(), domain.Order{SupplierAmount: "48.00"}, AllocationResult{SupplyScope: SupplyScopePublic}, "80.00")
	require.ErrorContains(t, err, "supplier settlement unavailable")
}
