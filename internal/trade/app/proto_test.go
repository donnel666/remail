package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type protoSuffixInventorySpy struct{ randomSuffixBatchInventorySpy }

func (*protoSuffixInventorySpy) ProtoProtocolReady() bool { return true }

func (*protoSuffixInventorySpy) ProtoAllocationReady(context.Context, string, uint) (bool, error) {
	return true, nil
}

func TestProtoCheckoutSuffixSelectionAndReplay(t *testing.T) {
	for _, productID := range []uint{0, 9} {
		for _, mode := range []domain.ServiceMode{domain.ServiceModeCode, domain.ServiceModePurchase} {
			for _, tc := range []struct {
				selector string
				suffix   string
				random   int
			}{
				{selector: "proto", suffix: "proton.me", random: 1},
				{selector: "proton.me", suffix: "proton.me"},
				{selector: " @PROTONMAIL.COM ", suffix: "protonmail.com"},
			} {
				t.Run(fmt.Sprintf("%d/%s/%s", productID, mode, tc.selector), func(t *testing.T) {
					repo := &batchRepoSpy{orders: map[string]domain.Order{}}
					inventory := &protoSuffixInventorySpy{randomSuffixBatchInventorySpy{
						checkoutInventorySpy: checkoutInventorySpy{available: true},
						allocationType:       domain.AllocationTypeProto, selectedSuffix: tc.suffix, successful: 1,
					}}
					wallet := &batchWalletSpy{}
					uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeProto}, wallet, inventory,
						&issuedOrderTokenSpy{tokens: map[string]*OrderToken{}})
					request := batchRequest("proto-suffix", 1)
					request.ProductID = productID
					request.EmailSuffix = tc.selector
					request.ServiceMode = string(mode)
					request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

					result, err := uc.Checkout(context.Background(), request)
					require.NoError(t, err)
					require.Equal(t, domain.ProductTypeProto, result.Order.ProductType)
					require.Equal(t, domain.AllocationTypeProto, *result.Order.AllocationType)
					require.Equal(t, "batch-1@"+tc.suffix, result.Order.DeliveryEmail)
					require.Equal(t, []string{tc.suffix}, inventory.allocationSuffixes)
					require.Equal(t, tc.random, inventory.selectionCalls)
					prepared, err := prepareCheckoutRequest(request)
					require.NoError(t, err)
					require.NoError(t, finalizeCheckoutProduct(&prepared, domain.ProductTypeProto))
					require.Equal(t, prepared.fingerprint, result.Order.RequestFingerprint, "random resolution must not change the request fingerprint")

					replayed, err := uc.Checkout(context.Background(), request)
					require.NoError(t, err)
					require.Equal(t, result.Order.OrderNo, replayed.Order.OrderNo)
					require.Equal(t, result.Order.DeliveryEmail, replayed.Order.DeliveryEmail)
					require.Equal(t, tc.random, inventory.selectionCalls, "replay must not select another suffix")
					require.Equal(t, 1, inventory.allocationCalls)
					require.Equal(t, 1, wallet.debits)
				})
			}
		}
	}
}

func TestProtoCheckoutBatchResolvesOneSuffixAndDoesNotRerollOnExhaustion(t *testing.T) {
	for _, selector := range []string{coredomain.RandomProtoSuffixSelector, "protonmail.com"} {
		t.Run(selector, func(t *testing.T) {
			repo := &batchRepoSpy{orders: map[string]domain.Order{}}
			inventory := &protoSuffixInventorySpy{randomSuffixBatchInventorySpy{
				checkoutInventorySpy: checkoutInventorySpy{available: true},
				allocationType:       domain.AllocationTypeProto, selectedSuffix: "protonmail.com", successful: 2,
			}}
			uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeProto}, &batchWalletSpy{}, inventory,
				&issuedOrderTokenSpy{tokens: map[string]*OrderToken{}})
			requests := make([]CheckoutRequest, 4)
			for i := range requests {
				requests[i] = batchRequest(fmt.Sprintf("proto-batch-%d", i), len(requests))
				requests[i].ProductID = 0
				requests[i].EmailSuffix = selector
				requests[i].SupplyPolicy = string(domain.SupplyPolicyPublicOnly)
			}

			items, err := uc.CheckoutBatch(context.Background(), requests)
			require.NoError(t, err)
			require.Len(t, items, 4)
			require.NoError(t, items[0].Err)
			require.NoError(t, items[1].Err)
			require.ErrorIs(t, items[2].Err, domain.ErrInsufficientInventory)
			require.ErrorIs(t, items[3].Err, domain.ErrInsufficientInventory)
			require.Equal(t, []string{"protonmail.com", "protonmail.com", "protonmail.com"}, inventory.allocationSuffixes)
			if selector == coredomain.RandomProtoSuffixSelector {
				require.Equal(t, 1, inventory.selectionCalls)
				require.Equal(t, selector, inventory.lastSelection.Selector)
			} else {
				require.Zero(t, inventory.selectionCalls)
			}
		})
	}
}

func TestProtoCheckoutRejectsUnsupportedSuffix(t *testing.T) {
	for _, suffix := range []string{"gmail.com", "outlook", "domain", "proto.me", "buyer@proton.me"} {
		for _, productID := range []uint{0, 9} {
			request := batchRequest("proto-invalid", 1)
			request.ProductID, request.EmailSuffix = productID, suffix
			prepared, err := prepareCheckoutRequest(request)
			if err == nil {
				err = finalizeCheckoutProduct(&prepared, domain.ProductTypeProto)
			}
			require.ErrorIs(t, err, domain.ErrInvalidOrderRequest, suffix)
		}
	}
}

type protoReplayQuery struct {
	channel                  domain.ClientChannel
	userID, apiKey           uint
	key, fingerprint, legacy string
}

type protoReplayRepo struct {
	*batchPreloadRepoSpy
	rechecking bool
	queries    []protoReplayQuery
}

func (r *protoReplayRepo) FindOrderByIdempotency(ctx context.Context, channel domain.ClientChannel, userID uint, apiKeyID *uint, key, fingerprint, legacy string) (*domain.Order, error) {
	if r.rechecking {
		r.queries = append(r.queries, protoReplayQuery{channel, userID, apiKeyFingerprint(apiKeyID), key, fingerprint, legacy})
	}
	order, err := r.batchRepoSpy.FindOrderByIdempotency(ctx, channel, userID, apiKeyID, key, fingerprint, legacy)
	if err != nil || order == nil {
		return order, err
	}
	// Match the real repository: the generic batch spy deliberately ignores
	// identity/fingerprints and would otherwise hide an unsafe replay lookup.
	if order.UserID != userID || order.ClientChannel != channel || apiKeyFingerprint(order.APIKeyID) != apiKeyFingerprint(apiKeyID) {
		return nil, nil
	}
	if order.ProductType == domain.ProductTypeLegacyRandom {
		fingerprint = legacy
	}
	if order.RequestFingerprint != fingerprint {
		return nil, domain.ErrIdempotencyConflict
	}
	return order, nil
}

type protoRandomMissInventory struct {
	*protoSuffixInventorySpy
	beforeMiss func(context.Context)
}

func (s *protoRandomMissInventory) SelectRandomSuffix(ctx context.Context, cmd RandomSuffixSelectionCommand) (string, error) {
	selected, err := s.protoSuffixInventorySpy.SelectRandomSuffix(ctx, cmd)
	if s.beforeMiss == nil {
		return selected, err
	}
	beforeMiss := s.beforeMiss
	s.beforeMiss = nil
	beforeMiss(ctx)
	return "", domain.ErrInsufficientInventory
}

func (s *protoRandomMissInventory) Allocate(ctx context.Context, cmd AllocationCommand) (*AllocationResult, error) {
	// Like the real allocator, a persisted but unallocated pending order can
	// still carry its original random selector into fulfillment.
	if cmd.EmailSuffix == coredomain.RandomProtoSuffixSelector {
		cmd.EmailSuffix = s.selectedSuffix
	}
	return s.protoSuffixInventorySpy.Allocate(ctx, cmd)
}

func TestProtoRandomInventoryMissReplaysConcurrentOrders(t *testing.T) {
	for _, channel := range []domain.ClientChannel{domain.ClientChannelConsole, domain.ClientChannelAPIKey} {
		for _, tc := range []struct {
			name                         string
			quantity, preloaded, winners int
			productID                    uint
		}{
			{"single winner", 1, 0, 1, 0}, {"single true shortage", 1, 0, 0, 0},
			{"single winner with product ID", 1, 0, 1, 9},
			{"batch winners", 3, 1, 2, 0}, {"batch with one genuinely missing key", 3, 1, 1, 0},
		} {
			t.Run(string(channel)+"/"+tc.name, func(t *testing.T) {
				base := &batchRepoSpy{orders: map[string]domain.Order{}}
				repo := &protoReplayRepo{batchPreloadRepoSpy: &batchPreloadRepoSpy{batchRepoSpy: base}}
				inventory := &protoRandomMissInventory{protoSuffixInventorySpy: &protoSuffixInventorySpy{randomSuffixBatchInventorySpy: randomSuffixBatchInventorySpy{
					checkoutInventorySpy: checkoutInventorySpy{available: true}, allocationType: domain.AllocationTypeProto,
					selectedSuffix: "protonmail.com", successful: tc.preloaded + tc.winners,
				}}}
				wallet, tokens := &batchWalletSpy{}, &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
				uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeProto}, wallet, inventory, tokens)
				requests := make([]CheckoutRequest, tc.quantity)
				for i := range requests {
					requests[i] = batchRequest(fmt.Sprintf("proto-race-%d", i), tc.quantity)
					requests[i].ProductID, requests[i].EmailSuffix, requests[i].ClientChannel = tc.productID, coredomain.RandomProtoSuffixSelector, channel
					requests[i].SupplyPolicy = string(domain.SupplyPolicyPublicOnly)
					if channel == domain.ClientChannelAPIKey {
						keyID := uint(41)
						requests[i].APIKeyID = &keyID
					}
				}
				for _, request := range requests[:tc.preloaded] {
					_, err := uc.Checkout(context.Background(), request)
					require.NoError(t, err)
				}
				selectionsAfterWinner := 0
				inventory.beforeMiss = func(ctx context.Context) {
					// Real checkout calls commit between the loser's initial lookup
					// and its zero-inventory result; no goroutine timing is needed.
					for _, request := range requests[tc.preloaded : tc.preloaded+tc.winners] {
						winner, err := uc.Checkout(ctx, request)
						require.NoError(t, err)
						require.Equal(t, domain.OrderStatusActive, winner.Order.Status)
					}
					selectionsAfterWinner = inventory.selectionCalls
					repo.rechecking = true
				}
				var items []CheckoutBatchItem
				if tc.quantity == 1 {
					result, err := uc.Checkout(context.Background(), requests[0])
					items = []CheckoutBatchItem{{Result: result, Err: err}}
				} else {
					var err error
					items, err = uc.CheckoutBatch(context.Background(), requests)
					require.NoError(t, err)
				}
				require.Len(t, items, tc.quantity)
				for i, item := range items {
					if i >= tc.preloaded+tc.winners {
						require.ErrorIs(t, item.Err, domain.ErrInsufficientInventory)
						require.Nil(t, item.Result)
						continue
					}
					require.NoError(t, item.Err)
					require.NotNil(t, item.Result)
					require.Equal(t, base.orders[requests[i].IdempotencyKey].OrderNo, item.Result.Order.OrderNo)
					require.Equal(t, "batch-"+fmt.Sprint(i+1)+"@protonmail.com", item.Result.Order.DeliveryEmail)
					require.False(t, item.Result.Created)
				}
				var expectedQueries []protoReplayQuery
				for _, request := range requests[tc.preloaded:] {
					prepared, err := prepareCheckoutRequest(request)
					require.NoError(t, err)
					require.NoError(t, finalizeCheckoutProduct(&prepared, domain.ProductTypeProto))
					expectedQueries = append(expectedQueries, protoReplayQuery{channel, request.UserID, apiKeyFingerprint(request.APIKeyID),
						request.IdempotencyKey, prepared.fingerprint, checkoutPreparationFingerprint(prepared, "")})
				}
				require.Equal(t, expectedQueries, repo.queries)
				require.Equal(t, selectionsAfterWinner, inventory.selectionCalls, "loser must not reroll")
				require.Equal(t, tc.preloaded+tc.winners, inventory.allocationCalls)
				require.Equal(t, tc.preloaded+tc.winners, wallet.debits)
				require.Equal(t, tc.preloaded+tc.winners, tokens.issues)
				require.Len(t, base.orders, tc.preloaded+tc.winners)
			})
		}
	}
}

func TestProtoRandomInventoryMissPreservesRecheckFailures(t *testing.T) {
	lookupErr := errors.New("winner lookup database error")
	for _, tc := range []struct {
		name                string
		quantity, preloaded int
		failure             string
		wantErr             error
	}{
		{"single fingerprint conflict", 1, 0, "fingerprint", domain.ErrIdempotencyConflict},
		{"batch base-key conflict", 2, 0, "fingerprint", domain.ErrIdempotencyConflict},
		{"batch tail conflict preserves successful head", 2, 1, "fingerprint", domain.ErrIdempotencyConflict},
		{"single database error", 1, 0, "database", lookupErr},
		{"batch database error", 2, 1, "database", lookupErr},
		{"recheck validates stored product", 1, 0, "product", domain.ErrInvalidOrderRequest},
		{"another API key is not a matching replay", 1, 0, "api_key", domain.ErrInsufficientInventory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &batchRepoSpy{orders: map[string]domain.Order{}}
			repo := &protoReplayRepo{batchPreloadRepoSpy: &batchPreloadRepoSpy{batchRepoSpy: base}}
			inventory := &protoRandomMissInventory{protoSuffixInventorySpy: &protoSuffixInventorySpy{randomSuffixBatchInventorySpy: randomSuffixBatchInventorySpy{
				checkoutInventorySpy: checkoutInventorySpy{available: true}, allocationType: domain.AllocationTypeProto,
				selectedSuffix: "proton.me", successful: tc.preloaded + 1,
			}}}
			wallet, tokens := &batchWalletSpy{}, &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}}
			uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeProto}, wallet, inventory, tokens)
			requests := make([]CheckoutRequest, tc.quantity)
			keyID := uint(41)
			for i := range requests {
				requests[i] = batchRequest(fmt.Sprintf("proto-recheck-%d", i), tc.quantity)
				requests[i].ProductID, requests[i].EmailSuffix = 0, coredomain.RandomProtoSuffixSelector
				requests[i].ClientChannel, requests[i].APIKeyID = domain.ClientChannelAPIKey, &keyID
				requests[i].SupplyPolicy = string(domain.SupplyPolicyPublicOnly)
			}
			for _, request := range requests[:tc.preloaded] {
				_, err := uc.Checkout(context.Background(), request)
				require.NoError(t, err)
			}
			selectionsAfterWinner := 0
			inventory.beforeMiss = func(ctx context.Context) {
				request := requests[tc.preloaded]
				switch tc.failure {
				case "fingerprint":
					request.ServiceMode = string(domain.ServiceModeCode)
				case "api_key":
					otherKey := uint(42)
					request.APIKeyID = &otherKey
				}
				winner, err := uc.Checkout(ctx, request)
				require.NoError(t, err)
				require.Equal(t, domain.OrderStatusActive, winner.Order.Status)
				switch tc.failure {
				case "database":
					base.findErrors = map[string]error{request.IdempotencyKey: lookupErr}
				case "product":
					order := base.orders[request.IdempotencyKey]
					order.ProductType = domain.ProductTypeGmail
					base.orders[request.IdempotencyKey] = order
				}
				selectionsAfterWinner = inventory.selectionCalls
				repo.rechecking = true
			}
			if tc.quantity == 1 {
				result, err := uc.Checkout(context.Background(), requests[0])
				require.ErrorIs(t, err, tc.wantErr)
				require.Nil(t, result)
				if tc.failure == "database" {
					require.NotErrorIs(t, err, domain.ErrInsufficientInventory)
				}
			} else {
				items, err := uc.CheckoutBatch(context.Background(), requests)
				if tc.failure == "fingerprint" && tc.preloaded == 0 {
					require.ErrorIs(t, err, domain.ErrIdempotencyConflict)
					require.Nil(t, items)
				} else {
					require.NoError(t, err)
					require.Len(t, items, tc.quantity)
					for i, item := range items {
						if tc.failure != "database" && i < tc.preloaded {
							require.NoError(t, item.Err)
							require.Equal(t, base.orders[requests[i].IdempotencyKey].OrderNo, item.Result.Order.OrderNo)
						} else {
							require.ErrorIs(t, item.Err, tc.wantErr)
							require.NotErrorIs(t, item.Err, domain.ErrInsufficientInventory)
							require.Nil(t, item.Result)
						}
					}
				}
			}
			require.Len(t, repo.queries, 1)
			require.Equal(t, selectionsAfterWinner, inventory.selectionCalls)
			require.Equal(t, tc.preloaded+1, wallet.debits)
			require.Equal(t, tc.preloaded+1, inventory.allocationCalls)
			require.Equal(t, tc.preloaded+1, tokens.issues)
			require.Len(t, base.orders, tc.preloaded+1)
		})
	}
}

func TestProtoRandomInventoryMissResumesTheStoredOrderState(t *testing.T) {
	for _, status := range []domain.OrderStatus{domain.OrderStatusPendingPayment, domain.OrderStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			base := &batchRepoSpy{orders: map[string]domain.Order{}}
			repo := &protoReplayRepo{batchPreloadRepoSpy: &batchPreloadRepoSpy{batchRepoSpy: base}}
			inventory := &protoRandomMissInventory{protoSuffixInventorySpy: &protoSuffixInventorySpy{randomSuffixBatchInventorySpy: randomSuffixBatchInventorySpy{
				checkoutInventorySpy: checkoutInventorySpy{available: true}, allocationType: domain.AllocationTypeProto,
				selectedSuffix: "protonmail.com", successful: 1,
			}}}
			wallet := &batchWalletSpy{}
			uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeProto}, wallet, inventory, &issuedOrderTokenSpy{tokens: map[string]*OrderToken{}})
			request := batchRequest("proto-stored-state", 1)
			request.ProductID, request.EmailSuffix = 0, coredomain.RandomProtoSuffixSelector
			inventory.beforeMiss = func(context.Context) {
				prepared, err := prepareCheckoutRequest(request)
				require.NoError(t, err)
				require.NoError(t, finalizeCheckoutProduct(&prepared, domain.ProductTypeProto))
				order := batchOrder(request.IdempotencyKey, status, domain.OrderFailureInsufficientBalance)
				order.ProductType, order.RequestFingerprint = domain.ProductTypeProto, prepared.fingerprint
				base.orders[request.IdempotencyKey] = order
				repo.rechecking = true
			}
			result, err := uc.Checkout(context.Background(), request)
			if status == domain.OrderStatusFailed {
				require.ErrorIs(t, err, domain.ErrInsufficientBalance, "replay keeps the stored failure, not the random precheck error")
				require.Equal(t, domain.OrderStatusFailed, result.Order.Status)
				require.Zero(t, wallet.debits)
				require.Zero(t, inventory.allocationCalls)
			} else {
				require.NoError(t, err)
				require.Equal(t, domain.OrderStatusActive, result.Order.Status)
				require.Equal(t, 1, wallet.debits)
				require.Equal(t, 1, inventory.allocationCalls)
			}
			require.Equal(t, "order-"+request.IdempotencyKey, result.Order.OrderNo)
			require.False(t, result.Created)
			require.Len(t, base.orders, 1)
			require.Len(t, repo.queries, 1)
			require.Equal(t, 1, inventory.selectionCalls, "stored orders must not rerun the trade random precheck")
		})
	}
}

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
