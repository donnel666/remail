package infra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLoadOrCreatePendingOrderRejectsLegacyRandom(t *testing.T) {
	order, created, err := NewRepo(nil).LoadOrCreatePendingOrder(context.Background(), tradeapp.CreatePendingOrderCommand{
		ProductType: domain.ProductTypeLegacyRandom,
	})

	require.ErrorIs(t, err, domain.ErrInvalidOrderRequest)
	require.Nil(t, order)
	require.False(t, created)
}

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

func TestCompleteGmailCodeOrderUsesSharedReadDeadline(t *testing.T) {
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
	require.Equal(t, readUntil, *completed.AfterSaleUntil)
}

func TestGmailCodeLifecycleIgnoresRetiredSessionRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:trade-gmail-code-lifecycle?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&OrderModel{}))
	require.NoError(t, db.Exec(`CREATE TABLE gmail_code_sessions (
		order_no TEXT PRIMARY KEY, source TEXT NOT NULL, status TEXT NOT NULL
	)`).Error)
	past := time.Now().UTC().Add(-time.Minute)
	for _, order := range []OrderModel{
		{OrderNo: "GMAIL-NEW-ACTIVE", ProductType: string(domain.ProductTypeGmail), ServiceMode: string(domain.ServiceModeCode), Status: string(domain.OrderStatusActive), ReceiveUntil: &past},
		{OrderNo: "GMAIL-RETIRED-ACTIVE", ProductType: string(domain.ProductTypeGmail), ServiceMode: string(domain.ServiceModeCode), Status: string(domain.OrderStatusActive), ReceiveUntil: &past},
		{OrderNo: "GMAIL-NEW-COMPLETED", ProductType: string(domain.ProductTypeGmail), ServiceMode: string(domain.ServiceModeCode), Status: string(domain.OrderStatusCompleted), ServiceCleanupStatus: "none", AfterSaleUntil: &past},
		{OrderNo: "GMAIL-RETIRED-COMPLETED", ProductType: string(domain.ProductTypeGmail), ServiceMode: string(domain.ServiceModeCode), Status: string(domain.OrderStatusCompleted), ServiceCleanupStatus: "none", AfterSaleUntil: &past},
	} {
		require.NoError(t, db.Create(&order).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO gmail_code_sessions(order_no, source, status)
		VALUES ('GMAIL-RETIRED-ACTIVE', 'local', 'unknown'),
		       ('GMAIL-RETIRED-COMPLETED', 'local', 'unknown')`).Error)
	repo := NewRepo(db)

	expired, err := repo.ListExpiredCodeOrderNos(context.Background(), time.Now().UTC(), 20)
	require.NoError(t, err)
	require.Equal(t, []string{"GMAIL-NEW-ACTIVE", "GMAIL-RETIRED-ACTIVE"}, expired)
	cleanup, err := repo.ListCodeOrderNosReadyForCleanup(context.Background(), time.Now().UTC(), 20)
	require.NoError(t, err)
	require.Equal(t, []string{"GMAIL-NEW-COMPLETED", "GMAIL-RETIRED-COMPLETED"}, cleanup)
}
