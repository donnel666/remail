package app

import (
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMicrosoftEnterpriseOnlyUsesMainAndTenantSuffixes(t *testing.T) {
	config := ProductAllocationConfig{ProductType: coredomain.ProductTypeMicrosoftEnterprise, MainWeight: 1, DotWeight: 9, PlusWeight: 9}
	require.Equal(t, []domain.MicrosoftMailbox{domain.MicrosoftMailboxMain}, microsoftMailboxPreferences("order", config))
	require.True(t, isRandomSuffixSelector(coredomain.RandomMicrosoftEnterpriseSuffixSelector))
	require.True(t, randomSuffixMatchesProduct(coredomain.RandomMicrosoftEnterpriseSuffixSelector, config.ProductType, "tenant.onmicrosoft.com"))
	require.False(t, randomSuffixMatchesProduct(coredomain.RandomMicrosoftEnterpriseSuffixSelector, config.ProductType, "outlook.com"))
	require.False(t, randomSuffixMatchesProduct(coredomain.RandomMicrosoftSuffixSelector, config.ProductType, "tenant.onmicrosoft.com"))
}
