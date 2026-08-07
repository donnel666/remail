package app

import (
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

func TestAllocationRequiredUntilCoversActivationThenWarranty(t *testing.T) {
	order := domain.Order{
		ServiceMode:             domain.ServiceModePurchase,
		ActivationWindowMinutes: 30,
		WarrantyMinutes:         15,
	}
	started := time.Now().UTC()
	until := allocationRequiredUntil(order)
	finished := time.Now().UTC()

	require.False(t, until.Before(started.Add(45*time.Minute)))
	require.False(t, until.After(finished.Add(45*time.Minute)))
}

func TestCompletedPurchaseKeepsAllocation(t *testing.T) {
	require.False(t, cleanupRetryShouldReleaseAllocation(domain.Order{
		ServiceMode: domain.ServiceModePurchase,
		Status:      domain.OrderStatusCompleted,
	}))
	require.True(t, cleanupRetryShouldReleaseAllocation(domain.Order{
		ServiceMode: domain.ServiceModeCode,
		Status:      domain.OrderStatusCompleted,
	}))
}
