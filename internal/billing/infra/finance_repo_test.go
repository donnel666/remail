package infra

import (
	"context"
	"testing"
	"time"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTotalSupplierAvailable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wallets []WalletModel
		want    string
	}{
		{name: "no wallets", want: "0"},
		{
			name:    "only super admin",
			wallets: []WalletModel{{UserID: 1, SupplierAvailable: "9000"}},
			want:    "0",
		},
		{
			name: "all supplier balances excluding super admin",
			wallets: []WalletModel{
				{UserID: 1, SupplierAvailable: "9000"},
				{UserID: 2, SupplierAvailable: "100.125001", SupplierFrozen: "50", ConsumerBalance: "80", CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
				{UserID: 9, SupplierAvailable: "25.625", SupplierFrozen: "90", ConsumerBalance: "99"},
				{UserID: 11, SupplierAvailable: "0", SupplierFrozen: "100", ConsumerBalance: "900"},
			},
			want: "125.750001",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, db.AutoMigrate(&WalletModel{}))
			if len(tc.wallets) > 0 {
				require.NoError(t, db.Create(&tc.wallets).Error)
			}

			total, err := billingapp.NewWalletUseCase(NewBillingRepo(db)).TotalSupplierAvailable(context.Background())

			require.NoError(t, err)
			amount, err := decimal.NewFromString(total)
			require.NoError(t, err)
			require.True(t, amount.Equal(decimal.RequireFromString(tc.want)), "got %s, want %s", total, tc.want)
		})
	}
}
