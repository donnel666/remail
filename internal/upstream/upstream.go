package upstream

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrUnavailable    = errors.New("upstream: unavailable")
	ErrPriceProtected = errors.New("upstream: price protected")
	ErrPickupInvalid  = errors.New("upstream: pickup credential mismatch")
)

type Strategy string

const (
	StrategyLocalFirst    Strategy = "local_first"
	StrategyUpstreamFirst Strategy = "upstream_first"
)

type EmailType string
type OrderType string

const (
	EmailTypeGmail    EmailType = "gmail"
	OrderTypeCode     OrderType = "code"
	OrderTypePurchase OrderType = "purchase"
)

type Demand struct {
	ProjectID uint
	ProductID uint
	BuyerID   uint
	EmailType EmailType
	OrderType OrderType
	PayAmount string
}

type SupplyQuote struct {
	Strategy  Strategy
	Available uint64
}

type PaidOrder struct {
	OrderNo   string
	ProjectID uint
	ProductID uint
	BuyerID   uint
	EmailType EmailType
	OrderType OrderType
	PayAmount string
	Selected  bool
}

type Activation struct {
	OrderNo   string
	Email     string
	StartedAt time.Time
	ExpiresAt time.Time
}

type Code struct {
	Seq        int
	Value      string
	ReceivedAt time.Time
}

type PickupRequest struct {
	OrderNo string
	Email   string
}

type PickupResult struct {
	Email         string
	Codes         []Code
	ReceivedCount int
	MaxCodes      int
	ExpiresAt     *time.Time
}

type Provider interface {
	Supply(context.Context, Demand) (*SupplyQuote, error)
	AcceptPaidOrder(context.Context, PaidOrder) (bool, error)
	OwnsOrder(context.Context, string) (bool, error)
	CancelOrder(context.Context, string) (bool, error)
	Pickup(context.Context, PickupRequest) (*PickupResult, bool, error)
}

type Offer struct{ provider Provider }

type Router struct{ providers []Provider }

func NewRouter(providers ...Provider) *Router {
	result := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			result = append(result, provider)
		}
	}
	return &Router{providers: result}
}

// Choose applies provider-level priority without exposing provider metadata to callers.
func (r *Router) Choose(ctx context.Context, demand Demand, localAvailable bool) (*Offer, bool, error) {
	if r == nil {
		if localAvailable {
			return nil, true, nil
		}
		return nil, false, ErrUnavailable
	}
	var upstreamFirst, localFirst Provider
	priceProtected := false
	var firstErr error
	for _, provider := range r.providers {
		quote, err := provider.Supply(ctx, demand)
		switch {
		case err == nil && quote != nil && quote.Available > 0:
			switch quote.Strategy {
			case StrategyUpstreamFirst:
				if upstreamFirst == nil {
					upstreamFirst = provider
				}
			case StrategyLocalFirst:
				if localFirst == nil {
					localFirst = provider
				}
			default:
				return nil, false, ErrUnavailable
			}
		case errors.Is(err, ErrUnavailable):
		case errors.Is(err, ErrPriceProtected):
			priceProtected = true
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if upstreamFirst != nil {
		return &Offer{provider: upstreamFirst}, false, nil
	}
	if localAvailable {
		return nil, true, nil
	}
	if localFirst != nil {
		return &Offer{provider: localFirst}, false, nil
	}
	if firstErr != nil {
		return nil, false, firstErr
	}
	if priceProtected {
		return nil, false, ErrPriceProtected
	}
	return nil, false, ErrUnavailable
}

func (r *Router) Owner(ctx context.Context, orderNo string) (*Offer, bool, error) {
	if r == nil || strings.TrimSpace(orderNo) == "" {
		return nil, false, nil
	}
	var firstErr error
	for _, provider := range r.providers {
		owned, err := provider.OwnsOrder(ctx, orderNo)
		if owned {
			return &Offer{provider: provider}, true, err
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return nil, false, firstErr
}

func (r *Router) AcceptPaidOrder(ctx context.Context, offer *Offer, order PaidOrder) error {
	if r == nil || offer == nil || offer.provider == nil {
		return ErrUnavailable
	}
	order.Selected = true
	handled, err := offer.provider.AcceptPaidOrder(ctx, order)
	if err != nil {
		return err
	}
	if !handled {
		return ErrUnavailable
	}
	return nil
}

func (r *Router) ResumePaidOrder(ctx context.Context, offer *Offer, order PaidOrder) error {
	if r == nil || offer == nil || offer.provider == nil {
		return ErrUnavailable
	}
	order.Selected = false
	handled, err := offer.provider.AcceptPaidOrder(ctx, order)
	if err != nil {
		return err
	}
	if !handled {
		return ErrUnavailable
	}
	return nil
}

func (r *Router) CancelOrder(ctx context.Context, orderNo string) (bool, error) {
	if r == nil {
		return false, nil
	}
	var firstErr error
	for _, provider := range r.providers {
		handled, err := provider.CancelOrder(ctx, orderNo)
		if handled {
			return true, err
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return false, firstErr
}

func (r *Router) Pickup(ctx context.Context, request PickupRequest) (*PickupResult, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	var firstErr error
	for _, provider := range r.providers {
		result, handled, err := provider.Pickup(ctx, request)
		if handled {
			return result, true, err
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return nil, false, firstErr
}
