package gmail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailImportAndSafeList(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-resources?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&localResourceModel{}))

	service := NewService(db, nil)
	result, err := service.ImportLocalResources(context.Background(), strings.Join([]string{
		"first@gmail.com----login password----JBSWY3DPEHPK3PXP----abcd efgh ijkl mnop",
		"invalid@outlook.com----password----secret----app-password",
		"first@gmail.com----duplicate----duplicate----duplicate",
	}, "\n"), "skip")
	require.NoError(t, err)
	require.Equal(t, &LocalResourceImportResult{Imported: 1, Skipped: 1, Invalid: 1, Total: 3}, result)

	var stored localResourceModel
	require.NoError(t, db.First(&stored).Error)
	require.Equal(t, "first@gmail.com", stored.Email)
	require.Equal(t, "login password", stored.Password)
	require.Equal(t, "JBSWY3DPEHPK3PXP", stored.TwoFactorSecret)
	require.Equal(t, "abcdefghijklmnop", stored.AppPassword)

	list, err := service.ListLocalResources(context.Background(), LocalResourceListFilter{Limit: 20})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.True(t, list.Items[0].PasswordConfigured)
	require.True(t, list.Items[0].TwoFactorConfigured)
	require.True(t, list.Items[0].AppPasswordConfigured)
	payload, err := json.Marshal(list)
	require.NoError(t, err)
	for _, secret := range []string{"login password", "JBSWY3DPEHPK3PXP", "abcdefghijklmnop"} {
		require.NotContains(t, string(payload), secret)
	}

	result, err = service.ImportLocalResources(context.Background(),
		"first@gmail.com----new-password----KRSXG5DSNFXGOIDB----new-app-password", "abort")
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)
	require.NoError(t, db.First(&stored, stored.ID).Error)
	require.Equal(t, "new-password", stored.Password)

	require.NoError(t, db.Model(&localResourceModel{}).Where("id = ?", stored.ID).Update("status", LocalResourceSold).Error)
	result, err = service.ImportLocalResources(context.Background(),
		"first@gmail.com----must-not-replace----MZXW6YTBOI======----must-not-replace", "skip")
	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.NoError(t, db.First(&stored, stored.ID).Error)
	require.Equal(t, "new-password", stored.Password)
	require.ErrorIs(t, service.SetLocalResourceEnabled(context.Background(), stored.ID, false), ErrLocalResourceBusy)
}

func TestLocalGmailImportAbortRejectsWholePayload(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-abort?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&localResourceModel{}))

	_, err = NewService(db, nil).ImportLocalResources(context.Background(), strings.Join([]string{
		"valid@gmail.com----password----JBSWY3DPEHPK3PXP----app-password",
		"bad@example.com----password----JBSWY3DPEHPK3PXP----app-password",
	}, "\n"), "abort")
	require.ErrorIs(t, err, ErrInvalidLocalResource)
	var count int64
	require.NoError(t, db.Model(&localResourceModel{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestLocalGmailImportCanonicalizesIdentityAndValidatesSecrets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-identity?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&localResourceModel{}))

	result, err := NewService(db, nil).ImportLocalResources(context.Background(), strings.Join([]string{
		"first.last@gmail.com----password----JBSW Y3DP EHPK 3PXP----abcd efgh ijkl mnop",
		"firstlast@googlemail.com----duplicate----JBSWY3DPEHPK3PXP----abcdefghijklmnop",
		"firstlast+tag@gmail.com----invalid----JBSWY3DPEHPK3PXP----abcdefghijklmnop",
	}, "\n"), "skip")
	require.NoError(t, err)
	require.Equal(t, &LocalResourceImportResult{Imported: 1, Skipped: 1, Invalid: 1, Total: 3}, result)

	var stored localResourceModel
	require.NoError(t, db.First(&stored).Error)
	require.Equal(t, "firstlast@gmail.com", stored.Identity)
	require.Equal(t, "JBSWY3DPEHPK3PXP", stored.TwoFactorSecret)
}

func TestLocalGmailPurchaseSellsOneResourceAndReturnsOnlyToOrderLookup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-purchase?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&localResourceModel{}))
	require.NoError(t, db.Exec(`CREATE TABLE orders (
		order_no TEXT PRIMARY KEY, product_type TEXT NOT NULL, service_mode TEXT NOT NULL, gmail_resource_id INTEGER
	)`).Error)
	require.NoError(t, db.Create(&localResourceModel{
		Email: "purchase@gmail.com", Identity: "purchase@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", Status: LocalResourceAvailable,
	}).Error)

	service := NewService(db, nil)
	delivery, err := service.AllocateLocalPurchase(context.Background(), tradeapp.GmailSupplyQuote{Source: SourceLocal})
	require.NoError(t, err)
	require.Equal(t, "purchase@gmail.com", delivery.Email)
	require.Equal(t, "password", delivery.Password)
	require.NoError(t, db.Exec(
		"INSERT INTO orders(order_no, product_type, service_mode, gmail_resource_id) VALUES ('PURCHASE-1', 'gmail', 'purchase', ?)",
		delivery.ResourceID,
	).Error)

	stored, err := service.FindLocalPurchase(context.Background(), "PURCHASE-1")
	require.NoError(t, err)
	require.Equal(t, delivery, stored)
	_, err = service.AllocateLocalPurchase(context.Background(), tradeapp.GmailSupplyQuote{Source: SourceLocal})
	require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory)
}
