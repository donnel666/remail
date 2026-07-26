package api

import (
	"testing"

	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestMapAllocationInvalidRequest(t *testing.T) {
	require.ErrorIs(t, mapAllocationError(allocdomain.ErrInvalidAllocationRequest), domain.ErrInvalidOrderRequest)
}

func TestApplyOrderingDiscountUsesLedgerPrecision(t *testing.T) {
	quote := &tradeapp.OrderingQuote{
		PayAmount:          "10.00",
		MicrosoftPayAmount: "0.123456",
		DomainPayAmount:    "0.08",
	}
	require.NoError(t, applyOrderingDiscount(quote, "0.90"))
	require.Equal(t, "9.00", quote.PayAmount)
	require.Equal(t, "0.11111", quote.MicrosoftPayAmount)
	require.Equal(t, "0.072", quote.DomainPayAmount)
	require.Error(t, applyOrderingDiscount(quote, "1.01"))
}
