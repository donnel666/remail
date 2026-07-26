package infra

import (
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
)

func TestRandomProductInventoryAddsMicrosoftAndDomainStock(t *testing.T) {
	stats := &allocapp.InventoryStats{
		Microsoft: allocapp.MicrosoftInventoryStats{TotalAvailable: 14},
		Domain:    allocapp.DomainInventoryStats{TotalAvailable: 7},
	}

	total := productInventoryTotalFromStats(productInventoryRow{Type: string(coredomain.ProductTypeRandom)}, stats)

	require.EqualValues(t, 21, total)
}
