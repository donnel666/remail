package infra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWithTxRunsRegisteredRollbackOnlyOnFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:trade-rollback-callback?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	repo := NewRepo(db)
	wantErr := errors.New("rollback")
	callbacks := 0

	err = repo.WithTx(context.Background(), func(ctx context.Context) error {
		require.True(t, platform.RegisterGormRollback(ctx, func(context.Context) error {
			callbacks++
			return nil
		}))
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, callbacks)

	require.NoError(t, repo.WithTx(context.Background(), func(ctx context.Context) error {
		require.True(t, platform.RegisterGormRollback(ctx, func(context.Context) error {
			callbacks++
			return nil
		}))
		return nil
	}))
	require.Equal(t, 1, callbacks)
}

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

func TestCompleteGmailCodeOrderPreservesWarrantyDeadline(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:trade-gmail-code-warranty?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&OrderModel{}, &OrderEventModel{}))

	startedAt := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	warrantyUntil := startedAt.Add(24 * time.Hour)
	readUntil := startedAt.Add(time.Hour)
	require.NoError(t, db.Create(&OrderModel{
		OrderNo: "GMAIL-WARRANTY-1", UserID: 1, ProjectID: 1, ProjectProductID: 1,
		ProductType: string(domain.ProductTypeGmail), ServiceMode: string(domain.ServiceModeCode),
		Status: string(domain.OrderStatusActive), PayAmount: "1", RefundAmount: "0",
		ReceiveStartedAt: &startedAt, ReceiveUntil: &warrantyUntil, AfterSaleUntil: &warrantyUntil,
	}).Error)

	completed, changed, err := NewRepo(db).CompleteCodeOrder(context.Background(), "GMAIL-WARRANTY-1", startedAt.Add(5*time.Minute), readUntil)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, domain.OrderStatusCompleted, completed.Status)
	require.Equal(t, readUntil, *completed.ReceiveUntil)
	require.Equal(t, warrantyUntil, *completed.AfterSaleUntil)
}
