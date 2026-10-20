package app

import (
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMicrosoftEnterpriseCheckoutSelectors(t *testing.T) {
	for _, suffix := range []string{"microsoft_enterprise", "tenant.onmicrosoft.com", "@TENANT.onmicrosoft.com"} {
		normalized, err := normalizeCheckoutProductSuffix(suffix)
		require.NoError(t, err)
		productType, err := checkoutProductTypeForSuffix(normalized)
		require.NoError(t, err)
		require.Equal(t, domain.ProductTypeMicrosoftEnterprise, productType)
		prepared := checkoutPreparation{selectedBySuffix: true, selectorSuffix: normalized}
		require.NoError(t, finalizeCheckoutProduct(&prepared, productType))
		require.Equal(t, normalized, prepared.emailSuffix)
	}
	for _, suffix := range []string{"outlook", "outlook.com", "tenant.onmicrosoft.com.evil.test"} {
		prepared := checkoutPreparation{emailSuffix: suffix}
		require.Error(t, finalizeCheckoutProduct(&prepared, domain.ProductTypeMicrosoftEnterprise))
	}
}
