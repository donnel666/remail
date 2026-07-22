package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type batchTxContextKey struct{}

type batchRepoSpy struct {
	Repository
	mu         sync.Mutex
	orders     map[string]domain.Order
	topTx      int
	nestedTx   int
	committed  int
	rolledBack int
	findErrors map[string]error
}

func (r *batchRepoSpy) WithTx(ctx context.Context, fn func(context.Context) error) error {
	r.mu.Lock()
	if ctx.Value(batchTxContextKey{}) != nil {
		r.nestedTx++
		r.mu.Unlock()
		return fn(ctx)
	}
	r.topTx++
	r.mu.Unlock()
	err := fn(context.WithValue(ctx, batchTxContextKey{}, true))
	r.mu.Lock()
	if err == nil {
		r.committed++
	} else {
		r.rolledBack++
	}
	r.mu.Unlock()
	return err
}

func (r *batchRepoSpy) FindOrderByIdempotency(_ context.Context, _ domain.ClientChannel, _ uint, _ *uint, idempotencyKey, _ string) (*domain.Order, error) {
	if err := r.findErrors[idempotencyKey]; err != nil {
		return nil, err
	}
	order := r.orders[idempotencyKey]
	return &order, nil
}

func (r *batchRepoSpy) LockOrderForUpdate(_ context.Context, orderNo string) (*domain.Order, error) {
	for _, order := range r.orders {
		if order.OrderNo == orderNo {
			copy := order
			return &copy, nil
		}
	}
	return nil, domain.ErrOrderNotFound
}

type batchWalletSpy struct {
	WalletPort
	mu    sync.Mutex
	locks int
}

func (w *batchWalletSpy) LockConsumer(ctx context.Context, _ uint) error {
	if ctx.Value(batchTxContextKey{}) == nil {
		return errors.New("wallet lock outside item transaction")
	}
	w.mu.Lock()
	w.locks++
	w.mu.Unlock()
	return nil
}

type batchTokenSpy struct{ OrderTokenPort }

func (batchTokenSpy) FindOrderTokenByOrder(_ context.Context, orderNo string) (*OrderToken, error) {
	return &OrderToken{TokenPlain: "token-" + orderNo}, nil
}

func batchOrder(key string, status domain.OrderStatus, failure domain.OrderFailureCode) domain.Order {
	return domain.Order{
		ID: 1, OrderNo: "order-" + key, UserID: 7, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeMicrosoft, ServiceMode: domain.ServiceModePurchase,
		SupplyPolicy: domain.SupplyPolicyPrivateFirst, Status: status, FailureCode: failure,
		PayAmount: "1.00", ActivationWindowMinutes: 10, WarrantyMinutes: 10,
		ClientChannel: domain.ClientChannelConsole, IdempotencyKey: key,
	}
}

func batchRequest(key string, quantity int) CheckoutRequest {
	return CheckoutRequest{
		UserID: 7, ProjectID: 8, ProductID: 9, BatchQuantity: quantity,
		ServiceMode: string(domain.ServiceModePurchase), SupplyPolicy: string(domain.SupplyPolicyPrivateFirst),
		ClientChannel: domain.ClientChannelConsole, IdempotencyKey: key,
	}
}

func TestCheckoutBatchUsesOneCommittedTransactionPerItemAndKeepsPartialSuccess(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{
		"first":  batchOrder("first", domain.OrderStatusActive, ""),
		"second": batchOrder("second", domain.OrderStatusFailed, domain.OrderFailureInsufficientInventory),
	}}
	wallet := &batchWalletSpy{}
	uc := NewUseCase(repo, nil, wallet, nil, batchTokenSpy{})

	items, err := uc.CheckoutBatch(context.Background(), []CheckoutRequest{
		batchRequest("first", 2), batchRequest("second", 2),
	})

	require.NoError(t, err)
	require.Len(t, items, 2)
	require.NoError(t, items[0].Err)
	require.Equal(t, "order-first", items[0].Result.Order.OrderNo)
	require.ErrorIs(t, items[1].Err, domain.ErrInsufficientInventory)
	require.Equal(t, 2, repo.topTx)
	require.Zero(t, repo.nestedTx)
	require.Equal(t, 2, repo.committed)
	require.Zero(t, repo.rolledBack)
	require.Equal(t, 2, wallet.locks)
}

func TestCheckoutBatchHandlesOneHundredItemsInOneBoundedCall(t *testing.T) {
	orders := make(map[string]domain.Order, 100)
	requests := make([]CheckoutRequest, 100)
	for i := range requests {
		key := fmt.Sprintf("item-%d", i)
		orders[key] = batchOrder(key, domain.OrderStatusActive, "")
		requests[i] = batchRequest(key, 100)
	}
	repo := &batchRepoSpy{orders: orders}
	uc := NewUseCase(repo, nil, &batchWalletSpy{}, nil, batchTokenSpy{})
	started := time.Now()

	items, err := uc.CheckoutBatch(context.Background(), requests)

	require.NoError(t, err)
	require.Len(t, items, 100)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, 100, repo.topTx)
	require.Equal(t, 100, repo.committed)
}

func TestCheckoutBatchContinuesAfterItemIdempotencyConflict(t *testing.T) {
	repo := &batchRepoSpy{
		orders: map[string]domain.Order{
			"first": batchOrder("first", domain.OrderStatusActive, ""),
			"third": batchOrder("third", domain.OrderStatusActive, ""),
		},
		findErrors: map[string]error{"second": domain.ErrIdempotencyConflict},
	}
	uc := NewUseCase(repo, nil, &batchWalletSpy{}, nil, batchTokenSpy{})

	items, err := uc.CheckoutBatch(context.Background(), []CheckoutRequest{
		batchRequest("first", 3), batchRequest("second", 3), batchRequest("third", 3),
	})

	require.NoError(t, err)
	require.Len(t, items, 3)
	require.NoError(t, items[0].Err)
	require.ErrorIs(t, items[1].Err, domain.ErrIdempotencyConflict)
	require.NoError(t, items[2].Err)
	require.Equal(t, 2, repo.topTx)
}

func TestCheckoutBatchStopsWhenBaseIdempotencyKeyConflicts(t *testing.T) {
	repo := &batchRepoSpy{
		orders:     map[string]domain.Order{"second": batchOrder("second", domain.OrderStatusActive, "")},
		findErrors: map[string]error{"first": domain.ErrIdempotencyConflict},
	}
	uc := NewUseCase(repo, nil, &batchWalletSpy{}, nil, batchTokenSpy{})

	items, err := uc.CheckoutBatch(context.Background(), []CheckoutRequest{
		batchRequest("first", 2), batchRequest("second", 2),
	})

	require.ErrorIs(t, err, domain.ErrIdempotencyConflict)
	require.Nil(t, items)
	require.Zero(t, repo.topTx)
}

func TestCheckoutBatchGateIsFIFOAndBounded(t *testing.T) {
	gate := newCheckoutBatchGate()
	releases := make([]func(), 0, checkoutBatchConcurrency)
	for userID := uint(1); userID <= checkoutBatchConcurrency; userID++ {
		release, err := gate.acquire(context.Background(), userID, 100)
		require.NoError(t, err)
		releases = append(releases, release)
	}

	type admission struct {
		userID  uint
		release func()
		err     error
	}
	admitted := make(chan admission, 2)
	for _, userID := range []uint{10, 11} {
		go func(userID uint) {
			release, err := gate.acquire(context.Background(), userID, 100)
			admitted <- admission{userID: userID, release: release, err: err}
		}(userID)
		require.Eventually(t, func() bool {
			gate.mu.Lock()
			defer gate.mu.Unlock()
			return len(gate.waiting) == int(userID-9)
		}, time.Second, time.Millisecond)
	}

	_, err := gate.acquire(context.Background(), 12, 1)
	require.ErrorIs(t, err, domain.ErrCheckoutOverloaded)
	_, err = gate.acquire(context.Background(), 10, 1)
	require.ErrorIs(t, err, domain.ErrCheckoutBusy)

	releases[0]()
	first := <-admitted
	require.NoError(t, first.err)
	require.Equal(t, uint(10), first.userID)
	first.release()
	second := <-admitted
	require.NoError(t, second.err)
	require.Equal(t, uint(11), second.userID)
	second.release()
	for _, release := range releases[1:] {
		release()
	}
}

func TestCheckoutBatchGateCancellationRemovesWaiter(t *testing.T) {
	gate := newCheckoutBatchGate()
	releases := make([]func(), 0, checkoutBatchConcurrency)
	for userID := uint(1); userID <= checkoutBatchConcurrency; userID++ {
		release, err := gate.acquire(context.Background(), userID, 1)
		require.NoError(t, err)
		releases = append(releases, release)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := gate.acquire(ctx, 10, 1)
		done <- err
	}()
	require.Eventually(t, func() bool {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		return len(gate.waiting) == 1
	}, time.Second, time.Millisecond)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	gate.mu.Lock()
	require.Empty(t, gate.waiting)
	require.Zero(t, gate.queuedUnits)
	_, exists := gate.users[10]
	gate.mu.Unlock()
	require.False(t, exists)
	for _, release := range releases {
		release()
	}
}

func TestCheckoutBatchMetricUsesBoundedQuantityClasses(t *testing.T) {
	tests := []struct {
		quantity int
		size     string
	}{{1, "001_020"}, {20, "001_020"}, {21, "021_040"}, {80, "061_080"}, {100, "081_100"}}
	for _, test := range tests {
		taskType, size := checkoutBatchMetric(test.quantity)
		require.Equal(t, test.size, size)
		require.Equal(t, "checkout_batch_"+test.size, taskType)
	}
}
