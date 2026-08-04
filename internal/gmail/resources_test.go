package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailSafeListHidesDeletedCredentials(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-resources?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}))
	for index, status := range []string{LocalResourceAvailable, LocalResourceDeleted} {
		root := resourceRootModel{Type: "gmail", OwnerUserID: 1, Version: 1}
		require.NoError(t, db.Create(&root).Error)
		require.NoError(t, db.Create(&localResourceModel{
			ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
			Email: fmt.Sprintf("safe-%d@gmail.com", index), Identity: fmt.Sprintf("safe-%d@gmail.com", index),
			Password: "login-password", TwoFactorSecret: "JBSWY3DPEHPK3PXP",
			AppPassword: "abcdefghijklmnop", Status: status,
		}).Error)
	}

	list, err := NewService(db, nil).ListLocalResources(context.Background(), LocalResourceListFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, "safe-0@gmail.com", list.Items[0].Email)
	require.True(t, list.Items[0].PasswordConfigured)
	require.True(t, list.Items[0].TwoFactorConfigured)
	require.True(t, list.Items[0].AppPasswordConfigured)
	require.EqualValues(t, 1, list.Facets.All)
	payload, err := json.Marshal(list)
	require.NoError(t, err)
	for _, secret := range []string{"login password", "JBSWY3DPEHPK3PXP", "abcdefghijklmnop"} {
		require.NotContains(t, string(payload), secret)
	}
}

func TestLocalGmailPurchaseSellsOneResourceAndReturnsOnlyToOrderLookup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-purchase?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &allocationModel{}))
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "purchase@gmail.com", Identity: "purchase@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", Status: LocalResourceAvailable,
	}).Error)

	service := NewService(db, nil)
	delivery, err := service.AllocateLocalPurchase(context.Background(), "PURCHASE-1", tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"})
	require.NoError(t, err)
	require.Equal(t, "purchase@gmail.com", delivery.Email)
	require.Equal(t, "password", delivery.Password)
	stored, err := service.FindLocalPurchase(context.Background(), "PURCHASE-1")
	require.NoError(t, err)
	require.Equal(t, delivery, stored)
	_, err = service.AllocateLocalPurchase(context.Background(), "PURCHASE-2", tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"})
	require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory)
}
