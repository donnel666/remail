package upstream

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type providerStub struct {
	quote         *SupplyQuote
	err           error
	owned         bool
	ownerErr      error
	cancelled     bool
	cancelErr     error
	pickup        *PickupResult
	pickupHandled bool
	pickupErr     error
}

func (s *providerStub) Supply(context.Context, Demand) (*SupplyQuote, error)   { return s.quote, s.err }
func (*providerStub) AcceptPaidOrder(context.Context, PaidOrder) (bool, error) { return true, nil }
func (s *providerStub) OwnsOrder(context.Context, string) (bool, error) {
	return s.owned, s.ownerErr
}
func (s *providerStub) CancelOrder(context.Context, string) (bool, error) {
	return s.cancelled, s.cancelErr
}
func (s *providerStub) Pickup(context.Context, PickupRequest) (*PickupResult, bool, error) {
	return s.pickup, s.pickupHandled, s.pickupErr
}

func TestRouterSkipsBrokenNonOwnerProviders(t *testing.T) {
	broken := errors.New("provider unavailable")
	healthy := &providerStub{
		quote:         &SupplyQuote{Strategy: StrategyUpstreamFirst, Available: 1},
		owned:         true,
		cancelled:     true,
		pickup:        &PickupResult{Email: "buyer@gmail.com"},
		pickupHandled: true,
	}
	router := NewRouter(&providerStub{err: broken, ownerErr: broken, cancelErr: broken, pickupErr: broken}, healthy)

	offer, local, err := router.Choose(context.Background(), Demand{}, true)
	require.NoError(t, err)
	require.False(t, local)
	require.Same(t, healthy, offer.provider)

	offer, owned, err := router.Owner(context.Background(), "ORDER-1")
	require.NoError(t, err)
	require.True(t, owned)
	require.Same(t, healthy, offer.provider)

	handled, err := router.CancelOrder(context.Background(), "ORDER-1")
	require.NoError(t, err)
	require.True(t, handled)

	pickup, handled, err := router.Pickup(context.Background(), PickupRequest{OrderNo: "ORDER-1"})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, "buyer@gmail.com", pickup.Email)
}

func TestRouterReturnsFirstProviderErrorWhenNothingCanHandle(t *testing.T) {
	first := errors.New("first provider failed")
	second := errors.New("second provider failed")
	router := NewRouter(&providerStub{err: first, ownerErr: first, cancelErr: first, pickupErr: first},
		&providerStub{err: second, ownerErr: second, cancelErr: second, pickupErr: second})

	_, _, err := router.Choose(context.Background(), Demand{}, false)
	require.ErrorIs(t, err, first)
	_, _, err = router.Owner(context.Background(), "ORDER-1")
	require.ErrorIs(t, err, first)
	_, err = router.CancelOrder(context.Background(), "ORDER-1")
	require.ErrorIs(t, err, first)
	_, _, err = router.Pickup(context.Background(), PickupRequest{OrderNo: "ORDER-1"})
	require.ErrorIs(t, err, first)
}

func TestChooseHonorsProviderPriorityAroundLocalSupply(t *testing.T) {
	tests := []struct {
		name           string
		provider       *providerStub
		localAvailable bool
		wantLocal      bool
		wantOffer      bool
		wantErr        error
	}{
		{name: "local first keeps local", provider: &providerStub{quote: &SupplyQuote{Strategy: StrategyLocalFirst, Available: 1}}, localAvailable: true, wantLocal: true},
		{name: "local first falls back upstream", provider: &providerStub{quote: &SupplyQuote{Strategy: StrategyLocalFirst, Available: 1}}, wantOffer: true},
		{name: "upstream first takes upstream", provider: &providerStub{quote: &SupplyQuote{Strategy: StrategyUpstreamFirst, Available: 1}}, localAvailable: true, wantOffer: true},
		{name: "empty upstream falls back local", provider: &providerStub{quote: &SupplyQuote{Strategy: StrategyUpstreamFirst}}, localAvailable: true, wantLocal: true},
		{name: "protected upstream reports protection when local empty", provider: &providerStub{err: ErrPriceProtected}, wantErr: ErrPriceProtected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offer, local, err := NewRouter(test.provider).Choose(context.Background(), Demand{}, test.localAvailable)
			require.ErrorIs(t, err, test.wantErr)
			require.Equal(t, test.wantLocal, local)
			require.Equal(t, test.wantOffer, offer != nil)
			if test.wantOffer {
				require.Same(t, test.provider, offer.provider)
			}
		})
	}

	localFirst := &providerStub{quote: &SupplyQuote{Strategy: StrategyLocalFirst, Available: 1}}
	upstreamFirst := &providerStub{quote: &SupplyQuote{Strategy: StrategyUpstreamFirst, Available: 1}}
	offer, local, err := NewRouter(localFirst, upstreamFirst).Choose(context.Background(), Demand{}, true)
	require.NoError(t, err)
	require.False(t, local)
	require.Same(t, upstreamFirst, offer.provider)
}
