package infra

import (
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/stretchr/testify/require"
)

func TestMapConsumerBalancesReturnsInvalidBalanceError(t *testing.T) {
	balances, err := mapConsumerBalances([]WalletModel{{UserID: 7, ConsumerBalance: "invalid"}})

	require.Nil(t, balances)
	require.ErrorIs(t, err, domain.ErrInvalidAmount)
	require.ErrorContains(t, err, "user 7")
}
