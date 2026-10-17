package app

import (
	"context"
	"fmt"
	"testing"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type daumSuffixInventorySpy struct{ randomSuffixBatchInventorySpy }

func (*daumSuffixInventorySpy) DaumProtocolReady() bool { return true }

func (*daumSuffixInventorySpy) DaumAllocationReady(context.Context, string, uint) (bool, error) {
	return true, nil
}

func TestDaumCheckoutSuffixSelectionAndReplay(t *testing.T) {
	for _, productID := range []uint{0, 9} {
		for _, mode := range []domain.ServiceMode{domain.ServiceModeCode, domain.ServiceModePurchase} {
			for _, tc := range []struct {
				selector string
				suffix   string
				random   int
			}{
				{selector: "daum", suffix: "daum.net", random: 1},
				{selector: "daum.net", suffix: "daum.net"},
				{selector: "hanmail2.net", suffix: "hanmail2.net"},
				{selector: " @HANMAIL.NET ", suffix: "hanmail.net"},
			} {
				t.Run(fmt.Sprintf("%d/%s/%s", productID, mode, tc.selector), func(t *testing.T) {
					repo := &batchRepoSpy{orders: map[string]domain.Order{}}
					inventory := &daumSuffixInventorySpy{randomSuffixBatchInventorySpy{
						checkoutInventorySpy: checkoutInventorySpy{available: true},
						allocationType:       domain.AllocationTypeDaum, selectedSuffix: tc.suffix, successful: 1,
					}}
					wallet := &batchWalletSpy{}
					uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeDaum}, wallet, inventory,
						&issuedOrderTokenSpy{tokens: map[string]*OrderToken{}})
					request := batchRequest("daum-suffix", 1)
					request.ProductID = productID
					request.EmailSuffix = tc.selector
					request.ServiceMode = string(mode)
					request.SupplyPolicy = string(domain.SupplyPolicyPublicOnly)

					result, err := uc.Checkout(context.Background(), request)
					require.NoError(t, err)
					require.Equal(t, domain.ProductTypeDaum, result.Order.ProductType)
					require.Equal(t, domain.AllocationTypeDaum, *result.Order.AllocationType)
					require.Equal(t, "batch-1@"+tc.suffix, result.Order.DeliveryEmail)
					require.Equal(t, []string{tc.suffix}, inventory.allocationSuffixes)
					require.Equal(t, tc.random, inventory.selectionCalls)
					prepared, err := prepareCheckoutRequest(request)
					require.NoError(t, err)
					require.NoError(t, finalizeCheckoutProduct(&prepared, domain.ProductTypeDaum))
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

func TestDaumCheckoutBatchResolvesOneSuffixAndDoesNotRerollOnExhaustion(t *testing.T) {
	for _, selector := range []string{coredomain.RandomDaumSuffixSelector, "hanmail.net"} {
		t.Run(selector, func(t *testing.T) {
			repo := &batchRepoSpy{orders: map[string]domain.Order{}}
			inventory := &daumSuffixInventorySpy{randomSuffixBatchInventorySpy{
				checkoutInventorySpy: checkoutInventorySpy{available: true},
				allocationType:       domain.AllocationTypeDaum, selectedSuffix: "hanmail.net", successful: 2,
			}}
			uc := NewUseCase(repo, &batchOrderingSpy{productType: domain.ProductTypeDaum}, &batchWalletSpy{}, inventory,
				&issuedOrderTokenSpy{tokens: map[string]*OrderToken{}})
			requests := make([]CheckoutRequest, 4)
			for i := range requests {
				requests[i] = batchRequest(fmt.Sprintf("daum-batch-%d", i), len(requests))
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
			require.Equal(t, []string{"hanmail.net", "hanmail.net", "hanmail.net"}, inventory.allocationSuffixes)
			if selector == coredomain.RandomDaumSuffixSelector {
				require.Equal(t, 1, inventory.selectionCalls)
				require.Equal(t, selector, inventory.lastSelection.Selector)
			} else {
				require.Zero(t, inventory.selectionCalls)
			}
		})
	}
}

func TestDaumCheckoutRejectsUnsupportedSuffix(t *testing.T) {
	for _, suffix := range []string{"gmail.com", "outlook", "domain", "daum.me", "buyer@daum.net"} {
		for _, productID := range []uint{0, 9} {
			request := batchRequest("daum-invalid", 1)
			request.ProductID, request.EmailSuffix = productID, suffix
			prepared, err := prepareCheckoutRequest(request)
			if err == nil {
				err = finalizeCheckoutProduct(&prepared, domain.ProductTypeDaum)
			}
			require.ErrorIs(t, err, domain.ErrInvalidOrderRequest, suffix)
		}
	}
}
