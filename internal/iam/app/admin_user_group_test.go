package app

import (
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func TestNormalizeUserGroupCapabilities(t *testing.T) {
	group := &domain.UserGroup{
		APIRPMLimit: 60, APIConcurrencyLimit: 3, APIQuotaLimit: 10000,
		PriceDiscountRatio: " 0.9 ", TopupThreshold: "10.123456",
	}
	require.NoError(t, normalizeUserGroupCapabilities(group))
	require.Equal(t, "0.90", group.PriceDiscountRatio)
	require.Equal(t, "10.123456", group.TopupThreshold)

	for _, invalid := range []domain.UserGroup{
		{APIRPMLimit: -1, PriceDiscountRatio: "1", TopupThreshold: "0"},
		{APIConcurrencyLimit: -1, PriceDiscountRatio: "1", TopupThreshold: "0"},
		{APIQuotaLimit: -1, PriceDiscountRatio: "1", TopupThreshold: "0"},
		{PriceDiscountRatio: "1.000001", TopupThreshold: "0"},
		{PriceDiscountRatio: "-0.1", TopupThreshold: "0"},
		{PriceDiscountRatio: "1", TopupThreshold: "-0.01"},
		{PriceDiscountRatio: "1", TopupThreshold: "0.0000001"},
	} {
		candidate := invalid
		require.ErrorIs(t, normalizeUserGroupCapabilities(&candidate), domain.ErrInvalidUserGroup)
	}
}
