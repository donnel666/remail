package infra

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRechargeTiersPreserveJSONNumberPrecision(t *testing.T) {
	tiers, err := rechargeTiers(`[1000000]`, `{"1000000":0.000001}`)
	require.NoError(t, err)
	require.Equal(t, "1000000.00", tiers[0].Points)
	require.Equal(t, "0.000001", tiers[0].BonusPoints)
}
