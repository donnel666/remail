package api

import (
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/gmail"
	"github.com/donnel666/remail/internal/smsbower"
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

func TestOverlaySMSBowerInventoryAddsOnlyGmailCodeSupply(t *testing.T) {
	localCode, localPurchase := int64(4), int64(2)
	snapshots := map[uint]*allocapp.ProjectProductInventoryTotals{
		7: {
			ProjectID: 7, TotalAvailable: 4,
			Items: []allocapp.ProductInventoryTotal{{
				ProductID: 70, TotalAvailable: 4, PublicAvailable: 4,
				CodeAvailable: &localCode, CodePublicAvailable: &localCode,
				PurchaseAvailable: &localPurchase, PurchasePublicAvailable: &localPurchase,
			}},
		},
	}

	overlaySMSBowerInventory(snapshots, []smsbower.InventoryItem{{ProjectID: 7, ProductID: 70, CodeAvailable: 3}})

	item := snapshots[7].Items[0]
	require.Equal(t, int64(7), *item.CodeAvailable)
	require.Equal(t, int64(7), *item.CodePublicAvailable)
	require.Equal(t, int64(2), *item.PurchaseAvailable)
	require.Equal(t, int64(2), *item.PurchasePublicAvailable)
	require.Equal(t, int64(7), item.TotalAvailable)
	require.Equal(t, int64(7), snapshots[7].TotalAvailable)
}
