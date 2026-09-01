package domain

import (
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMoneyPreservesSubCentAmounts(t *testing.T) {
	t.Parallel()

	amount, err := NormalizePositiveMoney("0.008")
	require.NoError(t, err)
	require.Equal(t, "0.008", amount)

	zero, err := NormalizeNonNegativeMoney("0")
	require.NoError(t, err)
	require.Equal(t, "0.00", zero)

	_, err = NormalizePositiveMoney("0.0000001")
	require.ErrorIs(t, err, ErrInvalidAmount)
}

func TestRechargeReconciliationWindowUsesSharedRuntimeSetting(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()[runtimeconfig.RechargeTimeoutMinutesKey]
	runtimeconfig.Delete(runtimeconfig.RechargeTimeoutMinutesKey)
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(runtimeconfig.RechargeTimeoutMinutesKey, previous)
		} else {
			runtimeconfig.Delete(runtimeconfig.RechargeTimeoutMinutesKey)
		}
	})

	require.Equal(t, 10*time.Minute, RechargeReconciliationWindow())
	runtimeconfig.Set(runtimeconfig.RechargeTimeoutMinutesKey, "12")
	require.Equal(t, 12*time.Minute, RechargeReconciliationWindow())
}
