package infra

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCompleteExpiredPurchaseRechecksCurrentDeadline(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	for _, tc := range []struct {
		name      string
		mode      domain.ServiceMode
		activated *time.Time
		receive   *time.Time
		warranty  *time.Time
		completed bool
	}{
		{"activation expired", domain.ServiceModePurchase, nil, &past, nil, true},
		{"activation future", domain.ServiceModePurchase, nil, &future, nil, false},
		{"late activation", domain.ServiceModePurchase, &past, &past, &future, false},
		{"warranty expired", domain.ServiceModePurchase, &past, &past, &past, true},
		{"missing receive deadline", domain.ServiceModePurchase, nil, nil, nil, false},
		{"missing warranty deadline", domain.ServiceModePurchase, &past, &past, nil, false},
		{"code uses delivery or refund", domain.ServiceModeCode, nil, &past, &past, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = pool.Close() })
			require.NoError(t, db.AutoMigrate(&OrderModel{}, &OrderEventModel{}))
			require.NoError(t, db.Create(&OrderModel{OrderNo: "CURRENT-DEADLINE", UserID: 1, Status: string(domain.OrderStatusActive), ServiceMode: string(tc.mode), ReceiveUntil: tc.receive, ActivatedAt: tc.activated, AfterSaleUntil: tc.warranty}).Error)
			current, changed, err := NewRepo(db).CompleteExpiredOrder(context.Background(), "CURRENT-DEADLINE", now, "Purchase window expired.")
			require.NoError(t, err)
			require.Equal(t, tc.completed, changed)
			status := domain.OrderStatusActive
			if tc.completed {
				status = domain.OrderStatusCompleted
			}
			require.Equal(t, status, current.Status)
		})
	}
}

func TestPurchaseActivationChecksDeadlineUnderOrderLock(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&OrderModel{}, &OrderEventModel{}))
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	deadline, warranty := now.Add(-time.Minute), now.Add(time.Hour)
	require.NoError(t, db.Create(&OrderModel{OrderNo: "LATE-MESSAGE", UserID: 1, Status: string(domain.OrderStatusCompleted), ServiceMode: string(domain.ServiceModePurchase), ReceiveUntil: &deadline}).Error)
	current, changed, err := NewRepo(db).ActivatePurchaseOrder(context.Background(), "LATE-MESSAGE", deadline.Add(time.Second), warranty, now)
	require.NoError(t, err)
	require.False(t, changed, "a message outside the locked order's activation window must not reopen it")
	require.Equal(t, domain.OrderStatusCompleted, current.Status)
	require.Nil(t, current.ActivatedAt)
}
