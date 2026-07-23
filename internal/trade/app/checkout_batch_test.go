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
	finds      int
	findsInTx  int
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

func (r *batchRepoSpy) FindOrderByIdempotency(ctx context.Context, _ domain.ClientChannel, _ uint, _ *uint, idempotencyKey, _ string) (*domain.Order, error) {
	r.mu.Lock()
	r.finds++
	if ctx.Value(batchTxContextKey{}) != nil {
		r.findsInTx++
	}
	r.mu.Unlock()
	if err := r.findErrors[idempotencyKey]; err != nil {
		return nil, err
	}
	order, exists := r.orders[idempotencyKey]
	if !exists {
		return nil, nil
	}
	return &order, nil
}

func (r *batchRepoSpy) LockOrderForUpdate(_ context.Context, orderNo string) (*domain.Order, error) {
	for _, order := range r.orders {
		if order.OrderNo == orderNo {
			orderCopy := order
			return &orderCopy, nil
		}
	}
	return nil, domain.ErrOrderNotFound
}

func (r *batchRepoSpy) LoadOrCreatePendingOrder(_ context.Context, cmd CreatePendingOrderCommand) (*domain.Order, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.orders[cmd.IdempotencyKey]; ok {
		existingCopy := existing
		return &existingCopy, false, nil
	}
	order := domain.Order{
		ID: uint(len(r.orders) + 1), OrderNo: cmd.OrderNo, UserID: cmd.UserID,
		ProjectID: cmd.ProjectID, ProjectProductID: cmd.ProjectProductID,
		ProductType: cmd.ProductType, ServiceMode: cmd.ServiceMode, SupplyPolicy: cmd.SupplyPolicy,
		Status: domain.OrderStatusPendingPayment, PayAmount: cmd.PayAmount,
		CodeWindowMinutes: cmd.CodeWindowMinutes, ActivationWindowMinutes: cmd.ActivationWindowMinutes,
		WarrantyMinutes: cmd.WarrantyMinutes, ClientChannel: cmd.ClientChannel,
		APIKeyID: cmd.APIKeyID, IdempotencyKey: cmd.IdempotencyKey,
	}
	r.orders[cmd.IdempotencyKey] = order
	return &order, true, nil
}

func (r *batchRepoSpy) MarkFailed(_ context.Context, cmd MarkFailedCommand) (*domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, order := range r.orders {
		if order.OrderNo != cmd.OrderNo {
			continue
		}
		order.Status = domain.OrderStatusFailed
		order.FailureCode = cmd.FailureCode
		r.orders[key] = order
		return &order, nil
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

type batchOrderingSpy struct {
	OrderingPort
	mu        sync.Mutex
	calls     int
	callsInTx int
}

func (s *batchOrderingSpy) GetOrderingQuote(ctx context.Context, projectID uint, productID uint, _ uint, _ domain.ServiceMode) (*OrderingQuote, error) {
	s.mu.Lock()
	s.calls++
	if ctx.Value(batchTxContextKey{}) != nil {
		s.callsInTx++
	}
	s.mu.Unlock()
	return &OrderingQuote{
		ProjectID: projectID, ProductID: productID, ProductType: domain.ProductTypeMicrosoft,
		PayAmount: "1.00", ActivationWindowMinutes: 10, WarrantyMinutes: 10,
	}, nil
}

type batchPreloadRepoSpy struct {
	*batchRepoSpy
	batchFinds     int
	batchFindsInTx int
}

func (r *batchPreloadRepoSpy) FindOrdersByIdempotencyBatch(
	ctx context.Context,
	_ domain.ClientChannel,
	_ uint,
	_ *uint,
	idempotencyKeys []string,
) (map[string]domain.Order, error) {
	r.mu.Lock()
	r.batchFinds++
	if ctx.Value(batchTxContextKey{}) != nil {
		r.batchFindsInTx++
	}
	r.mu.Unlock()
	orders := make(map[string]domain.Order, len(idempotencyKeys))
	for _, key := range idempotencyKeys {
		if order, exists := r.orders[key]; exists {
			orders[key] = order
		}
	}
	return orders, nil
}

type batchCancelTokenSpy struct {
	OrderTokenPort
	cancel context.CancelFunc
	once   sync.Once
}

type batchErrorTokenSpy struct {
	OrderTokenPort
	err error
}

type batchOrderErrorTokenSpy struct {
	OrderTokenPort
	orderNo string
	err     error
}

func (s batchErrorTokenSpy) FindOrderTokenByOrder(context.Context, string) (*OrderToken, error) {
	return nil, s.err
}

func (s batchOrderErrorTokenSpy) FindOrderTokenByOrder(_ context.Context, orderNo string) (*OrderToken, error) {
	if orderNo == s.orderNo {
		return nil, s.err
	}
	return &OrderToken{TokenPlain: "token-" + orderNo}, nil
}

type batchRetryRepoSpy struct {
	*batchRepoSpy
}

func (r *batchRetryRepoSpy) WithTx(ctx context.Context, fn func(context.Context) error) error {
	if ctx.Value(batchTxContextKey{}) != nil {
		return r.batchRepoSpy.WithTx(ctx, fn)
	}
	r.mu.Lock()
	r.topTx++
	r.mu.Unlock()
	txCtx := context.WithValue(ctx, batchTxContextKey{}, true)
	if err := fn(txCtx); err != nil {
		return err
	}
	r.mu.Lock()
	r.rolledBack++
	r.mu.Unlock()
	if err := fn(txCtx); err != nil {
		return err
	}
	r.mu.Lock()
	r.committed++
	r.mu.Unlock()
	return nil
}

func (s *batchCancelTokenSpy) FindOrderTokenByOrder(_ context.Context, orderNo string) (*OrderToken, error) {
	s.once.Do(s.cancel)
	return &OrderToken{TokenPlain: "token-" + orderNo}, nil
}

type checkoutAllocationErrorSpy struct {
	AllocationPort
	err error
}

func (s checkoutAllocationErrorSpy) Allocate(context.Context, AllocationCommand) (*AllocationResult, error) {
	return nil, s.err
}

type checkoutInventorySpy struct {
	AllocationPort
	available       bool
	err             error
	checks          int
	allocationCalls int
	marks           int
	marked          InventoryAvailabilityCommand
}

func (s *checkoutInventorySpy) MarkInventoryUnavailable(_ context.Context, cmd InventoryAvailabilityCommand) (bool, error) {
	s.marks++
	s.marked = cmd
	return true, nil
}

func (s *checkoutInventorySpy) HasAvailableInventory(context.Context, InventoryAvailabilityCommand) (bool, error) {
	s.checks++
	return s.available, s.err
}

func (s *checkoutInventorySpy) Allocate(context.Context, AllocationCommand) (*AllocationResult, error) {
	s.allocationCalls++
	return nil, domain.ErrInsufficientInventory
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

func TestPaidCheckoutStopsImmediatelyOnAllocationWriteError(t *testing.T) {
	wantErr := errors.New("allocation write conflict")
	uc := &UseCase{allocation: checkoutAllocationErrorSpy{err: wantErr}}

	result, err := uc.resumeCheckout(context.Background(), domain.Order{
		OrderNo: "order-1", UserID: 7, ProjectProductID: 9,
		SupplyPolicy: domain.SupplyPolicyPublicOnly, Status: domain.OrderStatusPaid,
	}, OrderingQuote{}, "", "")

	require.Nil(t, result)
	require.ErrorIs(t, err, wantErr)
}

func TestCheckoutRejectsZeroInventoryBeforeOpeningTransaction(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	inventory := &checkoutInventorySpy{}
	uc := NewUseCase(repo, &batchOrderingSpy{}, wallet, inventory, batchTokenSpy{})

	request := batchRequest("zero-inventory", 1)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)
	result, err := uc.Checkout(context.Background(), request)

	require.Nil(t, result)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	require.Equal(t, 1, inventory.checks)
	require.Zero(t, inventory.allocationCalls)
	require.Zero(t, repo.topTx)
	require.Zero(t, wallet.locks)
}

func TestCheckoutBatchChecksSharedZeroInventoryOnceAndReturnsEveryItem(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	inventory := &checkoutInventorySpy{}
	uc := NewUseCase(repo, &batchOrderingSpy{}, wallet, inventory, batchTokenSpy{})
	requests := make([]CheckoutRequest, 100)
	for i := range requests {
		requests[i] = batchRequest(fmt.Sprintf("zero-inventory-%03d", i), len(requests))
		requests[i].SupplyPolicy = string(domain.SupplyPolicyPublicOnly)
	}

	items, err := uc.CheckoutBatch(context.Background(), requests)

	require.NoError(t, err)
	require.Len(t, items, len(requests))
	for _, item := range items {
		require.ErrorIs(t, item.Err, domain.ErrInsufficientInventory)
		require.True(t, item.attempted)
	}
	require.Equal(t, 1, inventory.checks)
	require.Zero(t, inventory.allocationCalls)
	require.Zero(t, repo.topTx)
	require.Zero(t, wallet.locks)
}

func TestCheckoutInventoryPrecheckFailsOpenAndSkipsIdempotentReplay(t *testing.T) {
	wantErr := errors.New("inventory cache unavailable")
	inventory := &checkoutInventorySpy{err: wantErr}
	uc := &UseCase{allocation: inventory}
	prepared := []checkoutPreparation{
		{
			request: CheckoutRequest{UserID: 7}, policy: domain.SupplyPolicyPublicOnly,
			quote: &OrderingQuote{ProjectID: 8, ProductID: 9}, emailSuffix: "outlook.com",
		},
		{
			request: CheckoutRequest{UserID: 7}, existing: &domain.Order{ID: 1},
			quote: &OrderingQuote{ProjectID: 8, ProductID: 9},
		},
	}

	uc.precheckCheckoutInventory(context.Background(), prepared)

	require.NoError(t, prepared[0].prepareErr)
	require.NoError(t, prepared[1].prepareErr)
	require.Equal(t, 1, inventory.checks)
}

func TestCheckoutBatchMarksAllocatorExhaustionAndSkipsMatchingTail(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	inventory := &checkoutInventorySpy{available: true}
	uc := NewUseCase(repo, &batchOrderingSpy{}, wallet, inventory, batchTokenSpy{})
	requests := make([]CheckoutRequest, 100)
	for i := range requests {
		requests[i] = batchRequest(fmt.Sprintf("stale-inventory-%03d", i), len(requests))
		requests[i].SupplyPolicy = string(domain.SupplyPolicyPublicOnly)
	}

	items, err := uc.CheckoutBatch(context.Background(), requests)

	require.NoError(t, err)
	require.Len(t, items, len(requests))
	for _, item := range items {
		require.ErrorIs(t, item.Err, domain.ErrInsufficientInventory)
		require.True(t, item.attempted)
	}
	require.Equal(t, 1, inventory.checks)
	require.Equal(t, 1, inventory.allocationCalls)
	require.Equal(t, 1, inventory.marks)
	require.Equal(t, uint(8), inventory.marked.ProjectID)
	require.Equal(t, uint(9), inventory.marked.ProductID)
	require.Equal(t, 1, repo.topTx)
	require.Equal(t, 1, wallet.locks)
}

func TestCheckoutBatchDoesNotSkipPrivateFirstTailFromSharedPublicCorrection(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	inventory := &checkoutInventorySpy{available: true}
	uc := NewUseCase(repo, &batchOrderingSpy{}, wallet, inventory, batchTokenSpy{})
	requests := []CheckoutRequest{
		batchRequest("private-first-1", 2),
		batchRequest("private-first-2", 2),
	}

	items, err := uc.CheckoutBatch(context.Background(), requests)

	require.NoError(t, err)
	require.Len(t, items, len(requests))
	for _, item := range items {
		require.ErrorIs(t, item.Err, domain.ErrInsufficientInventory)
		require.True(t, item.attempted)
	}
	require.Equal(t, 1, inventory.checks)
	require.Equal(t, len(requests), inventory.allocationCalls)
	require.Equal(t, len(requests), inventory.marks)
	require.Equal(t, len(requests), repo.topTx)
	require.Equal(t, len(requests), wallet.locks)
}

func TestCheckoutBatchUsesIndependentItemTransactionsAndKeepsPartialSuccess(t *testing.T) {
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

func TestPrepareCheckoutBatchUsesOnePreloadAndOneQuoteOutsideTransaction(t *testing.T) {
	base := &batchRepoSpy{orders: map[string]domain.Order{}}
	repo := &batchPreloadRepoSpy{batchRepoSpy: base}
	ordering := &batchOrderingSpy{}
	uc := NewUseCase(repo, ordering, &batchWalletSpy{}, nil, batchTokenSpy{})

	prepared, err := uc.prepareCheckoutBatch(context.Background(), []CheckoutRequest{
		batchRequest("first", 2), batchRequest("second", 2),
	})

	require.NoError(t, err)
	require.Len(t, prepared, 2)
	require.Equal(t, 1, repo.batchFinds)
	require.Zero(t, repo.batchFindsInTx)
	require.Zero(t, base.finds)
	require.Zero(t, base.topTx)
	require.Equal(t, 1, ordering.calls)
	require.Zero(t, ordering.callsInTx)
	require.Same(t, prepared[0].quote, prepared[1].quote)
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
	require.Zero(t, repo.nestedTx)
	require.Equal(t, 100, repo.committed)
}

func TestCheckoutBatchKeepsCommittedItemsWhenRequestIsCanceled(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{
		"first":  batchOrder("first", domain.OrderStatusActive, ""),
		"second": batchOrder("second", domain.OrderStatusActive, ""),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	uc := NewUseCase(repo, nil, &batchWalletSpy{}, nil, &batchCancelTokenSpy{cancel: cancel})

	items, err := uc.CheckoutBatch(ctx, []CheckoutRequest{
		batchRequest("first", 2), batchRequest("second", 2),
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, items)
	require.Equal(t, 1, repo.topTx)
	require.Equal(t, 1, repo.committed)
	require.Zero(t, repo.rolledBack)
}

func TestCheckoutBatchReturnsFixedQuantityAfterItemInfrastructureRollback(t *testing.T) {
	wantErr := errors.New("database unavailable")
	repo := &batchRepoSpy{orders: map[string]domain.Order{
		"first":  batchOrder("first", domain.OrderStatusActive, ""),
		"second": batchOrder("second", domain.OrderStatusActive, ""),
	}}
	uc := NewUseCase(repo, nil, &batchWalletSpy{}, nil, batchErrorTokenSpy{err: wantErr})

	items, err := uc.CheckoutBatch(context.Background(), []CheckoutRequest{
		batchRequest("first", 2), batchRequest("second", 2),
	})

	require.NoError(t, err)
	require.Len(t, items, 2)
	for i := range items {
		require.Nil(t, items[i].Result)
		require.ErrorIs(t, items[i].Err, wantErr)
	}
	require.True(t, items[0].attempted)
	require.False(t, items[1].attempted)
	require.Equal(t, 1, repo.rolledBack)
}

func TestCheckoutBatchKeepsEarlierCommitWhenLaterItemHasInfrastructureFailure(t *testing.T) {
	wantErr := errors.New("database unavailable")
	repo := &batchRepoSpy{orders: map[string]domain.Order{
		"first":  batchOrder("first", domain.OrderStatusActive, ""),
		"second": batchOrder("second", domain.OrderStatusActive, ""),
		"third":  batchOrder("third", domain.OrderStatusActive, ""),
	}}
	uc := NewUseCase(repo, nil, &batchWalletSpy{}, nil, batchOrderErrorTokenSpy{
		orderNo: "order-second", err: wantErr,
	})

	items, err := uc.CheckoutBatch(context.Background(), []CheckoutRequest{
		batchRequest("first", 3), batchRequest("second", 3), batchRequest("third", 3),
	})

	require.NoError(t, err)
	require.Len(t, items, 3)
	require.NoError(t, items[0].Err)
	require.NotNil(t, items[0].Result)
	require.ErrorIs(t, items[1].Err, wantErr)
	require.Nil(t, items[1].Result)
	require.ErrorIs(t, items[2].Err, wantErr)
	require.Nil(t, items[2].Result)
	require.True(t, items[0].attempted)
	require.True(t, items[1].attempted)
	require.False(t, items[2].attempted)
	require.Equal(t, 2, repo.topTx)
	require.Equal(t, 1, repo.committed)
	require.Equal(t, 1, repo.rolledBack)
}

func TestCheckoutBatchResetsResultsWhenTransactionRetries(t *testing.T) {
	base := &batchRepoSpy{orders: map[string]domain.Order{
		"first":  batchOrder("first", domain.OrderStatusActive, ""),
		"second": batchOrder("second", domain.OrderStatusActive, ""),
	}}
	repo := &batchRetryRepoSpy{batchRepoSpy: base}
	uc := NewUseCase(repo, nil, &batchWalletSpy{}, nil, batchTokenSpy{})

	items, err := uc.CheckoutBatch(context.Background(), []CheckoutRequest{
		batchRequest("first", 2), batchRequest("second", 2),
	})

	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, 2, base.topTx)
	require.Equal(t, 2, base.rolledBack)
	require.Equal(t, 2, base.committed)
}

func TestCheckoutBatchMetricsKeepWorkUnitConservation(t *testing.T) {
	tests := []struct {
		name       string
		items      []CheckoutBatchItem
		runErr     error
		wantCounts [4]int
		wantResult string
	}{
		{
			name:       "succeeded",
			items:      []CheckoutBatchItem{{attempted: true}, {attempted: true}, {attempted: true}},
			wantCounts: [4]int{3, 0, 0, 0},
			wantResult: "succeeded",
		},
		{
			name: "partial",
			items: []CheckoutBatchItem{
				{attempted: true},
				{Err: domain.ErrInsufficientInventory, attempted: true},
				{Err: domain.ErrIdempotencyConflict, attempted: true},
			},
			wantCounts: [4]int{1, 2, 0, 0},
			wantResult: "partial",
		},
		{
			name:       "system failed",
			items:      []CheckoutBatchItem{{Err: errors.New("database unavailable"), attempted: true}, {Err: errors.New("database unavailable")}, {Err: errors.New("database unavailable")}},
			wantCounts: [4]int{0, 0, 1, 2},
			wantResult: "system_failed",
		},
		{
			name:       "preparation failed before item execution",
			items:      []CheckoutBatchItem{{Err: errors.New("database unavailable")}, {Err: errors.New("database unavailable")}, {Err: errors.New("database unavailable")}},
			wantCounts: [4]int{0, 0, 0, 3},
			wantResult: "system_failed",
		},
		{
			name:       "canceled",
			runErr:     context.Canceled,
			wantCounts: [4]int{0, 0, 0, 3},
			wantResult: "canceled",
		},
		{
			name:       "base idempotency conflict",
			runErr:     domain.ErrIdempotencyConflict,
			wantCounts: [4]int{0, 1, 0, 2},
			wantResult: "partial",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			succeeded, businessFailed, systemFailed, unprocessed := checkoutBatchCounts(3, tt.items, tt.runErr)
			got := [4]int{succeeded, businessFailed, systemFailed, unprocessed}

			require.Equal(t, tt.wantCounts, got)
			require.Equal(t, 3, succeeded+businessFailed+systemFailed+unprocessed)
			require.Equal(t, tt.wantResult, checkoutBatchServiceResult(businessFailed, systemFailed, unprocessed, tt.runErr))
		})
	}
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
	require.Zero(t, repo.nestedTx)
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
	require.Zero(t, repo.rolledBack)
}

func TestCheckoutBatchGateIsFIFOAndBounded(t *testing.T) {
	require.Equal(t, 1024, checkoutBatchConcurrency)
	require.Equal(t, 1024, checkoutBatchMaxWaiting)
	require.Equal(t, 5120, checkoutBatchMaxUnits)
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
	firstWaiterID := uint(checkoutBatchConcurrency + 1)
	for index, userID := range []uint{firstWaiterID, firstWaiterID + 1} {
		go func(userID uint) {
			release, err := gate.acquire(context.Background(), userID, 100)
			admitted <- admission{userID: userID, release: release, err: err}
		}(userID)
		require.Eventually(t, func() bool {
			gate.mu.Lock()
			defer gate.mu.Unlock()
			return len(gate.waiting) == index+1
		}, time.Second, time.Millisecond)
	}

	queuedUnits := 2 * ((100 + checkoutBatchUnitSize - 1) / checkoutBatchUnitSize)
	overloadQuantity := (checkoutBatchMaxUnits-queuedUnits)*checkoutBatchUnitSize + 1
	_, err := gate.acquire(context.Background(), firstWaiterID+2, overloadQuantity)
	require.ErrorIs(t, err, domain.ErrCheckoutOverloaded)
	_, err = gate.acquire(context.Background(), firstWaiterID, 1)
	require.ErrorIs(t, err, domain.ErrCheckoutBusy)

	releases[0]()
	first := <-admitted
	require.NoError(t, first.err)
	require.Equal(t, firstWaiterID, first.userID)
	first.release()
	second := <-admitted
	require.NoError(t, second.err)
	require.Equal(t, firstWaiterID+1, second.userID)
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
	waiterID := uint(checkoutBatchConcurrency + 1)
	go func() {
		_, err := gate.acquire(ctx, waiterID, 1)
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
	_, exists := gate.users[waiterID]
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
