package app

import (
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateRechargePaymentAmountForEpusdt(t *testing.T) {
	for _, raw := range []string{"0.00", "0.01", "0.0100"} {
		require.ErrorIs(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, raw), domain.ErrInvalidAmount, raw)
	}
	require.NoError(t, validateRechargePaymentAmount(domain.RechargePaymentMethodEpusdtUSDTTron, "0.02"))
	require.NoError(t, validateRechargePaymentAmount(domain.RechargePaymentMethodAlipay, "0.01"))
}
