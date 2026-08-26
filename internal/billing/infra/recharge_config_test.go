package infra

import (
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestRechargeTiersPreserveJSONNumberPrecision(t *testing.T) {
	tiers, err := rechargeTiers(`[1000000]`, `{"1000000":0.000001}`)
	require.NoError(t, err)
	require.Equal(t, "1000000.00", tiers[0].Points)
	require.Equal(t, "0.000001", tiers[0].BonusPoints)
}

func TestRechargeConfigReadsEpusdtMinimumPaymentAmount(t *testing.T) {
	const key = "epusdt_minimum_payment_amount"
	previous, existed := runtimeconfig.Snapshot()[key]
	runtimeconfig.Set(key, "12.50")
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(key, previous)
		} else {
			runtimeconfig.Delete(key)
		}
	})

	config, err := (RechargeConfigProvider{}).Current()
	require.NoError(t, err)
	require.Equal(t, "12.50", config.EpusdtMinimumPaymentAmount)
}
