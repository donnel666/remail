package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAttachOrderDeliverySummaryAllowsPurchaseMailWithoutCode(t *testing.T) {
	receivedAt := time.Date(2026, 7, 24, 12, 45, 0, 0, time.UTC)
	result := &CheckoutResult{}

	attachOrderDeliverySummary(result, OrderDeliverySummary{ReceivedAt: receivedAt})

	require.True(t, result.HasDelivery)
	require.Empty(t, result.VerificationCode)
	require.NotNil(t, result.LastMailReceivedAt)
	require.Equal(t, receivedAt, *result.LastMailReceivedAt)
}
