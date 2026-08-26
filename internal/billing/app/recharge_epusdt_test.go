package app

import (
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateRechargePaymentAmountForEpusdt(t *testing.T) {
	for _, raw := range []string{"", "0.00", "invalid"} {
		require.ErrorIs(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, raw, "10.00"), domain.ErrInvalidAmount, raw)
	}
	for _, raw := range []string{"0.01", "9.99"} {
		require.ErrorIs(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, raw, "10.00"), domain.ErrRechargePaymentBelowMinimum, raw)
	}
	require.NoError(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, "10.00", "10.00"))
	require.NoError(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, "0.02", "0.02"))
	require.NoError(t, validateRechargePaymentAmount(domain.RechargePaymentMethodAlipay, "0.01", ""))
	require.ErrorIs(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, "10.00", ""), domain.ErrRechargeConfigUnavailable)
	require.ErrorIs(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, "0.02", "0.01"), domain.ErrRechargeConfigUnavailable)
}
