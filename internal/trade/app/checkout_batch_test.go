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
	mu              sync.Mutex
	orders          map[string]domain.Order
	events          *[]string
	paidInTx        bool
	failedInTx      bool
	activeCalls     int
	activeConflicts int
	finds           int
	findsInTx       int
	topTx           int
	nestedTx        int
	committed       int
	rolledBack      int
	findErrors      map[string]error
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

func (r *batchRepoSpy) FindOrderByIdempotency(ctx context.Context, _ domain.ClientChannel, _ uint, _ *uint, idempotencyKey, _, _ string) (*domain.Order, error) {
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

func (r *batchRepoSpy) FindOrder(_ context.Context, orderNo string) (*domain.Order, error) {
	return r.LockOrderForUpdate(context.Background(), orderNo)
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
		RandomMicrosoftPayAmount: cmd.RandomMicrosoftPayAmount, RandomDomainPayAmount: cmd.RandomDomainPayAmount,
		CodeWindowMinutes: cmd.CodeWindowMinutes, ActivationWindowMinutes: cmd.ActivationWindowMinutes,
		WarrantyMinutes: cmd.WarrantyMinutes, ClientChannel: cmd.ClientChannel,
		APIKeyID: cmd.APIKeyID, IdempotencyKey: cmd.IdempotencyKey,
		RequestFingerprint: cmd.RequestFingerprint,
	}
	r.orders[cmd.IdempotencyKey] = order
	return &order, true, nil
}

func (r *batchRepoSpy) MarkFailed(ctx context.Context, cmd MarkFailedCommand) (*domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failedInTx = ctx.Value(batchTxContextKey{}) != nil
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

func (r *batchRepoSpy) MarkPaid(ctx context.Context, cmd MarkPaidCommand) (*domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		*r.events = append(*r.events, "mark_paid")
	}
	r.paidInTx = ctx.Value(batchTxContextKey{}) != nil
	for key, order := range r.orders {
		if order.OrderNo != cmd.OrderNo {
			continue
		}
		order.Status = domain.OrderStatusPaid
		order.PayAmount = cmd.PayAmount
		r.orders[key] = order
		return &order, nil
	}
	return nil, domain.ErrOrderNotFound
}

func (r *batchRepoSpy) MarkActive(_ context.Context, cmd MarkActiveCommand) (*domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeCalls++
	for key, order := range r.orders {
		if order.OrderNo != cmd.OrderNo {
			continue
		}
		order.Status = domain.OrderStatusActive
		order.AllocationType = &cmd.AllocationType
		order.DeliveryEmail = cmd.DeliveryEmail
		order.ReceiveStartedAt = &cmd.ReceiveStartedAt
		order.ReceiveUntil = &cmd.ReceiveUntil
		order.ActivatedAt = cmd.ActivatedAt
		order.AfterSaleUntil = cmd.AfterSaleUntil
		r.orders[key] = order
		if r.activeConflicts > 0 {
			r.activeConflicts--
			return nil, domain.ErrOrderStateConflict
		}
		return &order, nil
	}
	return nil, domain.ErrOrderNotFound
}

type batchWalletSpy struct {
	WalletPort
	mu            sync.Mutex
	events        *[]string
	balance       string
	balanceChecks int
	locks         int
	debits        int
	refunds       int
	debitErr      error
}

func (w *batchWalletSpy) ConsumerBalance(context.Context, uint) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.balanceChecks++
	if w.balance != "" {
		return w.balance, nil
	}
	return "1000.00", nil
}

func (w *batchWalletSpy) LockConsumer(ctx context.Context, _ uint) error {
	if ctx.Value(batchTxContextKey{}) == nil {
		return errors.New("wallet lock outside item transaction")
	}
	w.mu.Lock()
	if w.events != nil {
		*w.events = append(*w.events, "wallet_lock")
	}
	w.locks++
	w.mu.Unlock()
	return nil
}

func (w *batchWalletSpy) DebitConsumer(ctx context.Context, _ WalletCommand) (*WalletTransaction, error) {
	if ctx.Value(batchTxContextKey{}) == nil {
		return nil, errors.New("wallet debit outside item transaction")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.events != nil {
		*w.events = append(*w.events, "debit")
	}
	w.debits++
	if w.debitErr != nil {
		return nil, w.debitErr
	}
	return &WalletTransaction{ID: 1}, nil
}

func (w *batchWalletSpy) RefundConsumer(ctx context.Context, _ WalletCommand) (*WalletTransaction, error) {
	if ctx.Value(batchTxContextKey{}) == nil {
		return nil, errors.New("wallet refund outside item transaction")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refunds++
	return &WalletTransaction{ID: 2}, nil
}

type batchTokenSpy struct{ OrderTokenPort }

func (batchTokenSpy) FindOrderTokenByOrder(_ context.Context, orderNo string) (*OrderToken, error) {
	return &OrderToken{TokenPlain: "token-" + orderNo}, nil
}

type emptyOrderTokenSpy struct{ OrderTokenPort }

func (emptyOrderTokenSpy) FindOrderTokenByOrder(context.Context, string) (*OrderToken, error) {
	return nil, nil
}

type issuedOrderTokenSpy struct {
	OrderTokenPort
	tokens map[string]*OrderToken
	issues int
}

func (s *issuedOrderTokenSpy) FindOrderTokenByOrder(_ context.Context, orderNo string) (*OrderToken, error) {
	return s.tokens[orderNo], nil
}

func (s *issuedOrderTokenSpy) IssueOrderToken(_ context.Context, orderNo string, expireAt *time.Time) (*OrderToken, error) {
	if token := s.tokens[orderNo]; token != nil {
		return token, nil
	}
	s.issues++
	token := &OrderToken{TokenPlain: "token-" + orderNo, ExpireAt: expireAt}
	s.tokens[orderNo] = token
	return token, nil
}

type batchOrderingSpy struct {
	OrderingPort
	mu          sync.Mutex
	calls       int
	callsInTx   int
	productType domain.ProductType
}

func (s *batchOrderingSpy) GetOrderingQuote(ctx context.Context, projectID uint, productID uint, _ uint, _ domain.ServiceMode) (*OrderingQuote, error) {
	s.mu.Lock()
	s.calls++
	if ctx.Value(batchTxContextKey{}) != nil {
		s.callsInTx++
	}
	s.mu.Unlock()
	productType := s.productType
	if productType == "" {
		productType = domain.ProductTypeMicrosoft
	}
	quote := &OrderingQuote{
		ProjectID: projectID, ProductID: productID, ProductType: productType,
		PayAmount: "1.00", CodeWindowMinutes: 10, ActivationWindowMinutes: 10, WarrantyMinutes: 10,
	}
	if productType == domain.ProductTypeRandom {
		quote.PayAmount = "0.80"
		quote.MicrosoftPayAmount = "1.20"
		quote.DomainPayAmount = "0.80"
	}
	return quote, nil
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

type checkoutIssueTokenErrorSpy struct {
	OrderTokenPort
	err         error
	disables    int
	disableInTx bool
}

type batchOrderErrorTokenSpy struct {
	OrderTokenPort
	orderNo string
	err     error
}

func (s batchErrorTokenSpy) FindOrderTokenByOrder(context.Context, string) (*OrderToken, error) {
	return nil, s.err
}

func (s *checkoutIssueTokenErrorSpy) IssueOrderToken(context.Context, string, *time.Time) (*OrderToken, error) {
	return nil, s.err
}

func (s *checkoutIssueTokenErrorSpy) DisableOrderToken(ctx context.Context, _ string, _ string) error {
	s.disables++
	s.disableInTx = ctx.Value(batchTxContextKey{}) != nil
	return nil
}

func (s batchOrderErrorTokenSpy) FindOrderTokenByOrder(_ context.Context, orderNo string) (*OrderToken, error) {
	if orderNo == s.orderNo {
		return nil, s.err
	}
	return &OrderToken{TokenPlain: "token-" + orderNo}, nil
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
	allocation      *AllocationResult
	allocationErr   error
	lastAllocation  AllocationCommand
	events          *[]string
	allocationInTx  bool
	releaseInTx     bool
	checks          int
	allocationCalls int
	releaseCalls    int
	releasedOrderNo string
	marks           int
	marked          InventoryAvailabilityCommand
}

type checkoutGmailSupplySpy struct {
	checks      int
	creates     int
	finds       int
	schedules   int
	cancels     int
	sessionID   uint
	checkErr    error
	lastMode    domain.ServiceMode
	lastSession GmailSessionCommand
	purchases   int
	purchase    *GmailPurchaseDelivery
	purchaseErr error
}

func (s *checkoutGmailSupplySpy) CheckSupply(
	_ context.Context,
	_, _, _ uint,
	mode domain.ServiceMode,
	_ domain.SupplyPolicy,
	_ string,
) (*GmailSupplyQuote, error) {
	s.checks++
	s.lastMode = mode
	if s.checkErr != nil {
		return nil, s.checkErr
	}
	return &GmailSupplyQuote{Source: "local", CostPoints: "1"}, nil
}

func (s *checkoutGmailSupplySpy) FindSessionID(context.Context, string) (uint, error) {
	s.finds++
	return s.sessionID, nil
}

func (s *checkoutGmailSupplySpy) CreateSession(_ context.Context, cmd GmailSessionCommand) (uint, error) {
	s.creates++
	s.lastSession = cmd
	s.sessionID = 41
	return s.sessionID, nil
}

func (s *checkoutGmailSupplySpy) ScheduleProvision(context.Context, uint) error {
	s.schedules++
	return nil
}

func (s *checkoutGmailSupplySpy) CancelGmailOrder(context.Context, string) error {
	s.cancels++
	return nil
}

func (*checkoutGmailSupplySpy) ListGmailDeliveries(context.Context, []string) (map[string]GmailDeliverySummary, error) {
	return map[string]GmailDeliverySummary{}, nil
}

func (s *checkoutGmailSupplySpy) FindLocalPurchase(context.Context, string) (*GmailPurchaseDelivery, error) {
	s.purchases++
	if s.purchaseErr != nil {
		return nil, s.purchaseErr
	}
	if s.purchase == nil {
		s.purchase = &GmailPurchaseDelivery{
			AllocationID: 61, ResourceID: 51, SupplyScope: SupplyScopePublic, Email: "buyer@gmail.com", Password: "password",
			TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop",
		}
	}
	return s.purchase, nil
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

func (s *checkoutInventorySpy) Allocate(ctx context.Context, cmd AllocationCommand) (*AllocationResult, error) {
	s.allocationCalls++
	s.allocationInTx = ctx.Value(batchTxContextKey{}) != nil
	s.lastAllocation = cmd
	if s.events != nil {
		*s.events = append(*s.events, "allocate")
	}
	if s.allocationErr != nil {
		return nil, s.allocationErr
	}
	if s.allocation != nil {
		return s.allocation, nil
	}
	return nil, domain.ErrInsufficientInventory
}

func newCheckoutGmailInventorySpy() *checkoutInventorySpy {
	return &checkoutInventorySpy{allocation: &AllocationResult{
		Type: domain.AllocationTypeGmail, ID: 61, Email: "buyer@gmail.com", SupplyScope: SupplyScopePublic,
	}}
}

func (s *checkoutInventorySpy) ReleaseByOrder(ctx context.Context, orderNo string) error {
	s.releaseCalls++
	s.releaseInTx = ctx.Value(batchTxContextKey{}) != nil
	if s.events != nil {
		*s.events = append(*s.events, "release")
	}
	s.releasedOrderNo = orderNo
	return nil
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

func TestRandomCheckoutAmountUsesAllocatedProductPrice(t *testing.T) {
	order := domain.Order{
		ProjectID: 8, ProjectProductID: 9, ProductType: domain.ProductTypeRandom,
		ServiceMode: domain.ServiceModePurchase, PayAmount: "0.08",
		RandomMicrosoftPayAmount: "0.12", RandomDomainPayAmount: "0.08",
		ActivationWindowMinutes: 10, WarrantyMinutes: 10,
	}
	quote, err := orderingQuoteFromOrder(order)
	require.NoError(t, err)

	microsoftAmount, err := allocatedCheckoutPayAmount(order, *quote, AllocationResult{
		Type: domain.AllocationTypeMicrosoft, SupplyScope: SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "0.12", microsoftAmount)

	domainAmount, err := allocatedCheckoutPayAmount(order, *quote, AllocationResult{
		Type: domain.AllocationTypeDomain, SupplyScope: SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "0.08", domainAmount)

	ownedAmount, err := allocatedCheckoutPayAmount(order, *quote, AllocationResult{
		Type: domain.AllocationTypeMicrosoft, SupplyScope: SupplyScopeOwned,
	})
	require.NoError(t, err)
	require.Equal(t, "0.00", ownedAmount)

	order.RandomMicrosoftPayAmount = ""
	_, err = orderingQuoteFromOrder(order)
	require.ErrorIs(t, err, domain.ErrInvalidOrderRequest)
}

func TestCheckoutFingerprintIgnoresSuffixForProductsWithoutSuffixSelection(t *testing.T) {
	withSuffixRequest := batchRequest("same-key", 1)
	withSuffixRequest.EmailSuffix = "example.com"
	for _, productType := range []domain.ProductType{domain.ProductTypeRandom, domain.ProductTypeGmail} {
		withSuffix, err := prepareCheckoutRequest(withSuffixRequest)
		require.NoError(t, err)
		withoutSuffix, err := prepareCheckoutRequest(batchRequest("same-key", 1))
		require.NoError(t, err)
		require.NoError(t, finalizeCheckoutProduct(&withSuffix, productType))
		require.NoError(t, finalizeCheckoutProduct(&withoutSuffix, productType))
		require.Empty(t, withSuffix.emailSuffix)
		require.Equal(t, withoutSuffix.fingerprint, withSuffix.fingerprint)
	}

	for _, test := range []struct {
		productType domain.ProductType
		suffix      string
	}{
		{productType: domain.ProductTypeMicrosoft, suffix: "outlook.com"},
		{productType: domain.ProductTypeDomain, suffix: "com"},
	} {
		request := batchRequest("same-key", 1)
		request.EmailSuffix = test.suffix
		withSuffix, err := prepareCheckoutRequest(request)
		require.NoError(t, err)
		withoutSuffix, err := prepareCheckoutRequest(batchRequest("same-key", 1))
		require.NoError(t, err)
		require.NoError(t, finalizeCheckoutProduct(&withSuffix, test.productType))
		require.NoError(t, finalizeCheckoutProduct(&withoutSuffix, test.productType))
		require.NotEqual(t, withoutSuffix.fingerprint, withSuffix.fingerprint)
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

func TestCheckoutAllocatesBeforeOpeningShortPaymentTransaction(t *testing.T) {
	events := []string{}
	order := batchOrder("short-payment", domain.OrderStatusPendingPayment, "")
	repo := &batchRepoSpy{orders: map[string]domain.Order{"short-payment": order}, events: &events}
	wallet := &batchWalletSpy{events: &events}
	allocation := &checkoutInventorySpy{
		events: &events,
		allocation: &AllocationResult{
			Type: domain.AllocationTypeMicrosoft, ID: 1,
			Email: "allocated@example.com", SupplyScope: SupplyScopePublic,
		},
	}
	tokens := &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
	uc := NewUseCase(repo, nil, wallet, allocation, tokens)

	result, err := uc.resumeCheckout(context.Background(), order, OrderingQuote{
		ProjectID: order.ProjectID, ProductID: order.ProjectProductID,
		ProductType: order.ProductType, PayAmount: order.PayAmount,
		ActivationWindowMinutes: order.ActivationWindowMinutes, WarrantyMinutes: order.WarrantyMinutes,
	}, "", "request-1")

	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.Equal(t, []string{"allocate", "wallet_lock", "debit", "mark_paid"}, events)
	require.False(t, allocation.allocationInTx)
	require.True(t, repo.paidInTx)
	require.Equal(t, 1, repo.topTx)
}

func TestCheckoutCompensatesPaidFailureInOneShortTransaction(t *testing.T) {
	wantErr := errors.New("token store unavailable")
	order := batchOrder("token-failure", domain.OrderStatusPendingPayment, "")
	repo := &batchRepoSpy{orders: map[string]domain.Order{"token-failure": order}}
	wallet := &batchWalletSpy{}
	allocation := &checkoutInventorySpy{allocation: &AllocationResult{
		Type: domain.AllocationTypeMicrosoft, ID: 1,
		Email: "allocated@example.com", SupplyScope: SupplyScopePublic,
	}}
	tokens := &checkoutIssueTokenErrorSpy{err: wantErr}
	uc := NewUseCase(repo, nil, wallet, allocation, tokens)

	result, err := uc.resumeCheckout(context.Background(), order, OrderingQuote{
		ProjectID: order.ProjectID, ProductID: order.ProjectProductID,
		ProductType: order.ProductType, PayAmount: order.PayAmount,
		ActivationWindowMinutes: order.ActivationWindowMinutes, WarrantyMinutes: order.WarrantyMinutes,
	}, "", "request-1")

	require.Nil(t, result)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, domain.OrderStatusFailed, repo.orders["token-failure"].Status)
	require.Equal(t, domain.OrderFailureServiceToken, repo.orders["token-failure"].FailureCode)
	require.Equal(t, 1, wallet.debits)
	require.Equal(t, 1, wallet.refunds)
	require.Equal(t, 1, allocation.releaseCalls)
	require.True(t, allocation.releaseInTx)
	require.Equal(t, 1, tokens.disables)
	require.True(t, tokens.disableInTx)
	require.True(t, repo.failedInTx)
	require.Equal(t, 2, repo.topTx)
	require.Equal(t, 2, repo.committed)
	require.Zero(t, repo.rolledBack)
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

func TestCheckoutRejectsConcreteDomainBeforeInventoryPrecheck(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	inventory := &checkoutInventorySpy{available: true}
	ordering := &batchOrderingSpy{productType: domain.ProductTypeDomain}
	uc := NewUseCase(repo, ordering, wallet, inventory, batchTokenSpy{})
	request := batchRequest("concrete-domain", 1)
	request.EmailSuffix = "example.com"

	result, err := uc.Checkout(context.Background(), request)

	require.Nil(t, result)
	require.ErrorIs(t, err, domain.ErrInvalidOrderRequest)
	require.Equal(t, 1, ordering.calls)
	require.Zero(t, inventory.checks)
	require.Zero(t, wallet.balanceChecks)
	require.Zero(t, repo.topTx)
}

func TestCheckoutIgnoresRandomEmailSuffixBeforeInventoryPrecheck(t *testing.T) {
	ordering := &batchOrderingSpy{productType: domain.ProductTypeRandom}
	uc := NewUseCase(&batchRepoSpy{}, ordering, &batchWalletSpy{}, &checkoutInventorySpy{}, batchTokenSpy{})
	prepared := checkoutPreparation{
		request:     CheckoutRequest{UserID: 7, ProjectID: 8, ProductID: 9},
		mode:        domain.ServiceModePurchase,
		emailSuffix: "outlook.com",
	}

	err := uc.prepareCheckoutQuote(context.Background(), &prepared, nil)

	require.NoError(t, err)
	require.Empty(t, prepared.emailSuffix)
}

func TestGmailCodeCheckoutUsesLocalGmailAllocation(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	allocation := newCheckoutGmailInventorySpy()
	ordering := &batchOrderingSpy{productType: domain.ProductTypeGmail}
	supply := &checkoutGmailSupplySpy{}
	uc := NewUseCase(repo, ordering, wallet, allocation, batchTokenSpy{})
	uc.SetGmailPorts(supply, supply)
	request := batchRequest("gmail-code", 1)
	request.ServiceMode = string(domain.ServiceModeCode)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusPaid, result.Order.Status)
	require.Equal(t, 1, wallet.debits)
	require.Zero(t, allocation.checks)
	require.Equal(t, 1, allocation.allocationCalls)
	require.Equal(t, domain.ServiceModeCode, allocation.lastAllocation.ServiceMode)
	require.Equal(t, 1, supply.creates)
	require.Equal(t, 1, supply.schedules)
	require.Equal(t, "code_only", result.ContentMode)
	require.Equal(t, 3, result.MaxCodes)
	require.Equal(t, result.Order.OrderNo, supply.lastSession.OrderNo)
	require.Equal(t, result.Order.ProjectID, supply.lastSession.ProjectID)
	require.Equal(t, result.Order.ProjectProductID, supply.lastSession.ProductID)
	require.Equal(t, 10, supply.lastSession.CodeWindowMinutes)

	retried, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, result.Order.OrderNo, retried.Order.OrderNo)
	require.Equal(t, 1, wallet.debits)
	require.Equal(t, 1, supply.creates)
	require.Equal(t, 2, supply.schedules)
	require.Equal(t, 1, allocation.allocationCalls)
}

func TestGmailTerminalReplayReleasesUnifiedAllocation(t *testing.T) {
	for _, status := range []domain.OrderStatus{
		domain.OrderStatusFailed,
		domain.OrderStatusRefunded,
		domain.OrderStatusClosed,
	} {
		t.Run(string(status), func(t *testing.T) {
			allocation := &checkoutInventorySpy{}
			uc := NewUseCase(nil, nil, nil, allocation, nil)
			uc.SetGmailPorts(&checkoutGmailSupplySpy{}, nil)
			order := domain.Order{
				OrderNo: "gmail-terminal-" + string(status), ProductType: domain.ProductTypeGmail,
				ServiceMode: domain.ServiceModeCode, Status: status,
				FailureCode: domain.OrderFailureInsufficientInventory,
			}

			result, err := uc.checkoutGmailPrepared(context.Background(), checkoutPreparation{existing: &order})
			require.NotNil(t, result)
			if status == domain.OrderStatusFailed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, allocation.releaseCalls)
			require.Equal(t, order.OrderNo, allocation.releasedOrderNo)
		})
	}
}

func TestGmailLocalPurchaseChargesAndDeliversCredentialsOnce(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	ordering := &batchOrderingSpy{productType: domain.ProductTypeGmail}
	supply := &checkoutGmailSupplySpy{}
	tokens := &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
	allocation := newCheckoutGmailInventorySpy()
	uc := NewUseCase(repo, ordering, wallet, allocation, tokens)
	uc.SetGmailPorts(supply, supply)
	request := batchRequest("gmail-local-purchase", 1)

	result, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.EqualValues(t, 61, result.AllocationID)
	require.Equal(t, "buyer@gmail.com", result.Order.DeliveryEmail)
	require.Equal(t, "password", result.GmailPassword)
	require.Equal(t, "JBSWY3DPEHPK3PXP", result.GmailTwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", result.GmailAppPassword)
	require.Equal(t, "token-"+result.Order.OrderNo, result.ServiceToken)
	require.Equal(t, 1, wallet.debits)
	require.Equal(t, domain.ServiceModePurchase, allocation.lastAllocation.ServiceMode)
	require.Equal(t, 2, supply.purchases)
	require.Zero(t, supply.creates)
	require.Zero(t, supply.schedules)

	retried, err := uc.Checkout(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, result.Order.OrderNo, retried.Order.OrderNo)
	require.Equal(t, result.ServiceToken, retried.ServiceToken)
	require.Equal(t, "password", retried.GmailPassword)
	require.Equal(t, 1, wallet.debits)
	require.Equal(t, 3, supply.purchases)
	require.Equal(t, 1, tokens.issues)

	forbidden, err := uc.GetOrder(context.Background(), result.Order.OrderNo, 999, false)
	require.Nil(t, forbidden)
	require.ErrorIs(t, err, domain.ErrOrderForbidden)
	owner, err := uc.GetOrder(context.Background(), result.Order.OrderNo, result.Order.UserID, false)
	require.NoError(t, err)
	require.Equal(t, "password", owner.GmailPassword)
	require.Equal(t, result.ServiceToken, owner.ServiceToken)
	admin, err := uc.GetOrder(context.Background(), result.Order.OrderNo, 999, true)
	require.NoError(t, err)
	require.Equal(t, "abcdefghijklmnop", admin.GmailAppPassword)
}

func TestGmailLocalPurchaseReloadsAfterActivationStateConflict(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}, activeConflicts: 1}
	wallet := &batchWalletSpy{}
	supply := &checkoutGmailSupplySpy{}
	tokens := &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
	allocation := newCheckoutGmailInventorySpy()
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, allocation, tokens)
	uc.SetGmailPorts(supply, supply)

	result, err := uc.Checkout(context.Background(), batchRequest("gmail-activation-conflict", 1))

	require.NoError(t, err)
	require.Equal(t, domain.OrderStatusActive, result.Order.Status)
	require.Equal(t, "token-"+result.Order.OrderNo, result.ServiceToken)
	require.Equal(t, 1, repo.activeCalls)
	require.Equal(t, 1, wallet.debits)
	require.Zero(t, wallet.refunds)
	require.Zero(t, allocation.releaseCalls)
	require.Equal(t, 1, tokens.issues)
}

func TestGmailLocalPurchaseCompensatesServiceTokenFailureInOneShortTransaction(t *testing.T) {
	storeErr := errors.New("token store unavailable")
	for _, test := range []struct {
		name     string
		issueErr error
	}{
		{name: "store error", issueErr: storeErr},
		{name: "nil token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &batchRepoSpy{orders: map[string]domain.Order{}}
			wallet := &batchWalletSpy{}
			supply := &checkoutGmailSupplySpy{}
			tokens := &checkoutIssueTokenErrorSpy{err: test.issueErr}
			allocation := newCheckoutGmailInventorySpy()
			uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, allocation, tokens)
			uc.SetGmailPorts(supply, supply)

			result, err := uc.Checkout(context.Background(), batchRequest("gmail-token-failure", 1))

			require.Nil(t, result)
			if test.issueErr != nil {
				require.ErrorIs(t, err, test.issueErr)
			} else {
				require.EqualError(t, err, "issue order token returned no token")
			}
			require.Equal(t, domain.OrderStatusFailed, repo.orders["gmail-token-failure"].Status)
			require.Equal(t, domain.OrderFailureServiceToken, repo.orders["gmail-token-failure"].FailureCode)
			require.Equal(t, 1, wallet.debits)
			require.Equal(t, 1, wallet.refunds)
			require.Equal(t, 1, allocation.releaseCalls)
			require.True(t, allocation.releaseInTx)
			require.Equal(t, 1, tokens.disables)
			require.True(t, tokens.disableInTx)
			require.True(t, repo.failedInTx)
			require.Equal(t, 2, repo.topTx)
			require.Equal(t, 2, repo.committed)
			require.Zero(t, repo.rolledBack)
		})
	}
}

func TestGetHistoricalGmailPurchaseBackfillsServiceToken(t *testing.T) {
	order := domain.Order{
		ID: 1, OrderNo: "GMAIL-HISTORICAL", UserID: 7, ProjectID: 8, ProjectProductID: 9,
		ProductType: domain.ProductTypeGmail, ServiceMode: domain.ServiceModePurchase,
		Status: domain.OrderStatusActive, DeliveryEmail: "history@gmail.com",
		ActivationWindowMinutes: 10, WarrantyMinutes: 10,
	}
	repo := &batchRepoSpy{orders: map[string]domain.Order{"historical": order}}
	tokens := &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
	supply := &checkoutGmailSupplySpy{purchase: &GmailPurchaseDelivery{
		AllocationID: 61, ResourceID: 51, Email: order.DeliveryEmail, Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop",
	}}
	uc := NewUseCase(repo, &batchOrderingSpy{}, &batchWalletSpy{}, &checkoutInventorySpy{}, tokens)
	uc.SetGmailPorts(supply, supply)

	result, err := uc.GetOrder(context.Background(), order.OrderNo, order.UserID, false)

	require.NoError(t, err)
	require.Equal(t, "token-"+order.OrderNo, result.ServiceToken)
	require.Equal(t, 1, tokens.issues)
	replayed, err := uc.GetOrder(context.Background(), order.OrderNo, order.UserID, false)
	require.NoError(t, err)
	require.Equal(t, result.ServiceToken, replayed.ServiceToken)
	require.Equal(t, 1, tokens.issues)
}

func TestGmailLocalPurchaseRefundsOnceWhenInventoryDisappears(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	supply := &checkoutGmailSupplySpy{}
	allocation := newCheckoutGmailInventorySpy()
	allocation.allocation = nil
	allocation.allocationErr = domain.ErrInsufficientInventory
	uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeGmail}, wallet, allocation, emptyOrderTokenSpy{})
	uc.SetGmailPorts(supply, supply)
	request := batchRequest("gmail-local-empty", 1)

	result, err := uc.Checkout(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	require.Equal(t, domain.OrderStatusFailed, result.Order.Status)
	require.Zero(t, wallet.debits)
	require.Zero(t, wallet.refunds)
	require.Zero(t, supply.purchases)
	require.Equal(t, 1, allocation.allocationCalls)

	retried, err := uc.Checkout(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	require.Equal(t, result.Order.OrderNo, retried.Order.OrderNo)
	require.Zero(t, wallet.debits)
	require.Zero(t, wallet.refunds)
	require.Zero(t, supply.purchases)
	require.Equal(t, 1, allocation.allocationCalls)
}

func TestGmailPurchaseWithoutSupportedRouteStopsBeforeCharging(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{}
	allocation := &checkoutInventorySpy{available: true}
	ordering := &batchOrderingSpy{productType: domain.ProductTypeGmail}
	supply := &checkoutGmailSupplySpy{checkErr: domain.ErrUpstreamUnavailable}
	uc := NewUseCase(repo, ordering, wallet, allocation, batchTokenSpy{})
	uc.SetGmailPorts(supply, supply)

	result, err := uc.Checkout(context.Background(), batchRequest("gmail-purchase", 1))
	require.Nil(t, result)
	require.ErrorIs(t, err, domain.ErrUpstreamUnavailable)
	require.Equal(t, domain.ServiceModePurchase, supply.lastMode)
	require.Zero(t, wallet.debits)
	require.Zero(t, allocation.allocationCalls)
	require.Zero(t, repo.topTx)
}

func TestCheckoutBatchRejectsPrivateBalanceBelowPriceBeforeOpeningTransactions(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{balance: "0.01"}
	inventory := &checkoutInventorySpy{available: true}
	uc := NewUseCase(repo, &batchOrderingSpy{}, wallet, inventory, batchTokenSpy{})
	requests := []CheckoutRequest{batchRequest("low-balance-1", 2), batchRequest("low-balance-2", 2)}

	items, err := uc.CheckoutBatch(context.Background(), requests)

	require.NoError(t, err)
	require.Len(t, items, len(requests))
	for _, item := range items {
		require.Nil(t, item.Result)
		require.ErrorIs(t, item.Err, domain.ErrInsufficientBalance)
	}
	require.Equal(t, 1, wallet.balanceChecks)
	require.Zero(t, inventory.checks)
	require.Zero(t, repo.topTx)
	require.Zero(t, wallet.locks)
	require.Empty(t, repo.orders)
}

func TestCheckoutBatchBalancePrecheckReservesEarlierAmounts(t *testing.T) {
	wallet := &batchWalletSpy{balance: "1.00"}
	uc := &UseCase{wallet: wallet}
	prepared := []checkoutPreparation{
		{request: CheckoutRequest{UserID: 7}, quote: &OrderingQuote{PayAmount: "1.00"}},
		{request: CheckoutRequest{UserID: 7}, quote: &OrderingQuote{PayAmount: "1.00"}},
	}

	uc.precheckCheckoutBalance(context.Background(), prepared)

	require.NoError(t, prepared[0].prepareErr)
	require.ErrorIs(t, prepared[1].prepareErr, domain.ErrInsufficientBalance)
	require.Equal(t, 1, wallet.balanceChecks)
}

func TestCheckoutCommitsFailedOrderWhenBalanceDropsAfterPrecheck(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{balance: "1.00", debitErr: domain.ErrInsufficientBalance}
	inventory := &checkoutInventorySpy{
		available: true,
		allocation: &AllocationResult{
			Type: domain.AllocationTypeMicrosoft, ID: 1,
			Email: "allocated@example.com", SupplyScope: SupplyScopePublic,
		},
	}
	uc := NewUseCase(repo, &batchOrderingSpy{}, wallet, inventory, batchTokenSpy{})

	result, err := uc.Checkout(context.Background(), batchRequest("stale-balance", 1))

	require.ErrorIs(t, err, domain.ErrInsufficientBalance)
	require.NotNil(t, result)
	require.True(t, result.Created)
	stored := repo.orders["stale-balance"]
	require.Equal(t, domain.OrderStatusFailed, stored.Status)
	require.Equal(t, domain.OrderFailureInsufficientBalance, stored.FailureCode)
	require.Equal(t, 1, wallet.balanceChecks)
	require.Equal(t, 1, wallet.locks)
	require.Equal(t, 1, wallet.debits)
	require.Equal(t, 1, inventory.checks)
	require.Equal(t, 1, inventory.allocationCalls)
	require.Equal(t, 1, inventory.releaseCalls)
	require.Equal(t, stored.OrderNo, inventory.releasedOrderNo)
	require.False(t, inventory.allocationInTx)
	require.True(t, inventory.releaseInTx)
	require.Equal(t, 2, repo.topTx)
	require.Equal(t, 1, repo.committed)
	require.Equal(t, 1, repo.rolledBack)
}

func TestCheckoutIdempotentReplaySkipsZeroBalancePrecheck(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{
		"replay": batchOrder("replay", domain.OrderStatusActive, ""),
	}}
	wallet := &batchWalletSpy{balance: "0.00", debitErr: domain.ErrInsufficientBalance}
	ordering := &batchOrderingSpy{}
	inventory := &checkoutInventorySpy{}
	uc := NewUseCase(repo, ordering, wallet, inventory, batchTokenSpy{})

	result, err := uc.Checkout(context.Background(), batchRequest("replay", 1))

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Created)
	require.Equal(t, "order-replay", result.Order.OrderNo)
	require.Equal(t, "token-order-replay", result.ServiceToken)
	require.Zero(t, wallet.balanceChecks)
	require.Zero(t, wallet.debits)
	require.Zero(t, ordering.calls)
	require.Zero(t, inventory.checks)
	require.Zero(t, wallet.locks)
	require.Zero(t, repo.topTx)
	require.Zero(t, repo.committed)
	require.Zero(t, repo.rolledBack)
}

func TestCheckoutRejectsPublicPriceAboveBalanceBeforeOpeningTransaction(t *testing.T) {
	repo := &batchRepoSpy{orders: map[string]domain.Order{}}
	wallet := &batchWalletSpy{balance: "0.50"}
	inventory := &checkoutInventorySpy{available: true}
	uc := NewUseCase(repo, &batchOrderingSpy{}, wallet, inventory, batchTokenSpy{})
	request := batchRequest("low-public-balance", 1)
	request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

	result, err := uc.Checkout(context.Background(), request)

	require.Nil(t, result)
	require.ErrorIs(t, err, domain.ErrInsufficientBalance)
	require.Equal(t, 1, wallet.balanceChecks)
	require.Zero(t, inventory.checks)
	require.Zero(t, repo.topTx)
	require.Zero(t, wallet.locks)
	require.Empty(t, repo.orders)
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
	require.Zero(t, wallet.locks)
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
	require.Zero(t, wallet.locks)
}

func TestCheckoutBatchKeepsPartialSuccessWithoutLockingWalletsForTerminalOrders(t *testing.T) {
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
	require.Zero(t, repo.topTx)
	require.Zero(t, repo.nestedTx)
	require.Zero(t, repo.committed)
	require.Zero(t, repo.rolledBack)
	require.Zero(t, wallet.locks)
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
	require.Zero(t, repo.topTx)
	require.Zero(t, repo.nestedTx)
	require.Zero(t, repo.committed)
}

func TestCheckoutBatchStopsAfterRequestIsCanceled(t *testing.T) {
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
	require.Zero(t, repo.topTx)
	require.Zero(t, repo.committed)
	require.Zero(t, repo.rolledBack)
}

func TestCheckoutBatchReturnsFixedQuantityAfterItemInfrastructureFailure(t *testing.T) {
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
	require.Zero(t, repo.rolledBack)
}

func TestCheckoutBatchKeepsEarlierResultWhenLaterItemHasInfrastructureFailure(t *testing.T) {
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
	require.Zero(t, repo.topTx)
	require.Zero(t, repo.committed)
	require.Zero(t, repo.rolledBack)
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
	require.Zero(t, repo.topTx)
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

type allocationLookupStub struct {
	AllocationPort
	items map[string]AllocationResult
}

func (s allocationLookupStub) FindAllocationsByOrders(_ context.Context, _ []string) (map[string]AllocationResult, error) {
	return s.items, nil
}

func TestAttachAllocationIDsUsesOrderNumberAndAllowsUpstreamGmail(t *testing.T) {
	microsoft := domain.AllocationTypeMicrosoft
	gmail := domain.AllocationTypeGmail
	uc := &UseCase{allocation: allocationLookupStub{items: map[string]AllocationResult{
		"OR-MS": {OrderNo: "OR-MS", Type: microsoft, ID: 71},
	}}}
	microsoftResult := &CheckoutResult{Order: domain.Order{OrderNo: "OR-MS", AllocationType: &microsoft}}
	upstreamGmailResult := &CheckoutResult{Order: domain.Order{OrderNo: "OR-UPSTREAM", AllocationType: &gmail}}

	require.NoError(t, uc.attachAllocationIDs(context.Background(), microsoftResult, upstreamGmailResult))
	require.EqualValues(t, 71, microsoftResult.AllocationID)
	require.Zero(t, upstreamGmailResult.AllocationID)
}
