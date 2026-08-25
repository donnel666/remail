package infra

import (
	"context"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/donnel666/remail/internal/billing/domain"
)

// RechargeGatewayRouter selects a provider from the immutable order snapshot.
// It has no payment state of its own; only Query can report a paid order.
type RechargeGatewayRouter struct {
	epay   *EPay
	epusdt *Epusdt
}

func NewRechargeGateway(providers ...billingapp.RechargeConfigProvider) *RechargeGatewayRouter {
	var provider billingapp.RechargeConfigProvider
	if len(providers) > 0 {
		provider = providers[0]
	}
	return &RechargeGatewayRouter{epay: NewEPay(), epusdt: NewEpusdtWithConfigProvider(provider)}
}

func (router *RechargeGatewayRouter) gateway(config billingapp.RechargeConfig) billingapp.RechargeGateway {
	if config.Provider == "epusdt" || config.PaymentMethod == domain.RechargePaymentMethodEpusdtUSDTTron {
		return router.epusdt
	}
	if config.Provider == "" || config.Provider == "epay" || config.PaymentMethod == "" || config.PaymentMethod == domain.RechargePaymentMethodAlipay {
		return router.epay
	}
	return nil
}

func (router *RechargeGatewayRouter) PaymentURL(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge, clientIP string) (string, error) {
	if router == nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	gateway := router.gateway(config)
	if gateway == nil {
		return "", domain.ErrRechargeConfigUnavailable
	}
	return gateway.PaymentURL(ctx, config, recharge, clientIP)
}

func (router *RechargeGatewayRouter) Query(ctx context.Context, config billingapp.RechargeConfig, recharge domain.Recharge) (billingapp.RechargeGatewayQuery, error) {
	if router == nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
	gateway := router.gateway(config)
	if gateway == nil {
		return billingapp.RechargeGatewayQuery{}, domain.ErrRechargeConfigUnavailable
	}
	return gateway.Query(ctx, config, recharge)
}
