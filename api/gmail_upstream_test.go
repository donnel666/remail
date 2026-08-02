package api

import (
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/gmail"
	"github.com/stretchr/testify/require"
)

func TestOverlayGmailInventorySeparatesCodeAndPurchase(t *testing.T) {
	snapshots := map[uint]*allocapp.ProjectProductInventoryTotals{
		7: {
			ProjectID:      7,
			TotalAvailable: 4,
			Items: []allocapp.ProductInventoryTotal{
				{ProductID: 70, TotalAvailable: 4, PublicAvailable: 4},
				{ProductID: 71},
			},
		},
	}

	overlayGmailInventory(snapshots, []gmail.InventoryItem{{ProjectID: 7, ProductID: 71, CodeAvailable: 9}})

	require.Equal(t, int64(13), snapshots[7].TotalAvailable)
	require.Equal(t, int64(4), snapshots[7].Items[0].TotalAvailable)
	require.Equal(t, int64(9), snapshots[7].Items[1].TotalAvailable)
	require.Equal(t, int64(9), snapshots[7].Items[1].PublicAvailable)
	require.Equal(t, int64(9), *snapshots[7].Items[1].CodeAvailable)
	require.Equal(t, int64(9), *snapshots[7].Items[1].CodePublicAvailable)
	require.Zero(t, *snapshots[7].Items[1].PurchaseAvailable)
	require.Zero(t, *snapshots[7].Items[1].PurchasePublicAvailable)
}
