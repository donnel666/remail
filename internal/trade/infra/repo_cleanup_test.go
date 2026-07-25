package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCleanupRecoveryIncludesRefundedOrdersWithNoCleanupAttempt(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:trade-cleanup-recovery?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&OrderModel{}))
	for _, order := range []OrderModel{
		{OrderNo: "REFUNDED-NONE", Status: string(domain.OrderStatusRefunded), ServiceCleanupStatus: "none"},
		{OrderNo: "REFUNDED-PARTIAL", Status: string(domain.OrderStatusRefunded), ServiceCleanupStatus: "partial_failure"},
		{OrderNo: "REFUNDED-DONE", Status: string(domain.OrderStatusRefunded), ServiceCleanupStatus: "succeeded"},
		{OrderNo: "ACTIVE-NONE", Status: string(domain.OrderStatusActive), ServiceCleanupStatus: "none"},
	} {
		require.NoError(t, db.Create(&order).Error)
	}

	orderNos, err := NewRepo(db).ListPartialCleanupOrderNos(context.Background(), 200)

	require.NoError(t, err)
	require.ElementsMatch(t, []string{"REFUNDED-NONE", "REFUNDED-PARTIAL"}, orderNos)
}
