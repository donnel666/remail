package domain

import (
	"testing"

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
