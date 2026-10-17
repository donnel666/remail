package app

import (
	"testing"

	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func TestDaumProjectProductAndSuffixBlacklist(t *testing.T) {
	req := ProjectProductRequest{Type: "daum", Status: "enabled", CodeEnabled: true, PurchaseEnabled: true, CodeWindowMinutes: 10, ActivationWindowMinutes: 10, WarrantyMinutes: 10}
	products, err := normalizeProductRequests([]ProjectProductRequest{req}, true, true)
	require.NoError(t, err)
	require.Equal(t, domain.ProductTypeDaum, products[0].Type)
	require.Equal(t, 1, products[0].MainWeight)
	require.Zero(t, products[0].DotWeight)
	require.Zero(t, products[0].PlusWeight)
	require.Equal(t, "8.000000", products[0].CodePrice)
	blacklist, err := normalizeDaumSuffixBlacklist([]string{" @HANMAIL2.NET ", "daum.net"}, products)
	require.NoError(t, err)
	require.Equal(t, []string{"daum.net", "hanmail2.net"}, blacklist)
	for _, input := range [][]string{{"hotmail.com"}, {"hanmail.net", "@HANMAIL.NET"}, {""}} {
		_, err = normalizeDaumSuffixBlacklist(input, products)
		require.ErrorIs(t, err, domain.ErrInvalidProject)
	}
	req.DotWeight = 1
	_, err = normalizeProductRequests([]ProjectProductRequest{req}, true, true)
	require.ErrorIs(t, err, domain.ErrInvalidProduct)
	req.DotWeight = 0
	req.PlusWeight = 1
	_, err = normalizeProductRequests([]ProjectProductRequest{req}, true, true)
	require.ErrorIs(t, err, domain.ErrInvalidProduct)
}
