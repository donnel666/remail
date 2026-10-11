package infra

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSupplierSettlementCursorUsesDeadlineAndOrderNumber(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:supplier-settlement-cursor?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, stmt := range []string{
		"CREATE TABLE orders (order_no TEXT PRIMARY KEY, status TEXT, service_mode TEXT)",
		"CREATE TABLE supplier_settlements (order_no TEXT PRIMARY KEY, status TEXT, available_at DATETIME)",
		"CREATE TABLE aftersale_tickets (order_no TEXT, status TEXT)",
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	now := time.Now().UTC()
	for _, item := range []struct {
		no  string
		due time.Time
	}{
		{"Z", now.Add(-2 * time.Hour)}, {"A", now.Add(-time.Hour)}, {"B", now.Add(-time.Hour)}, {"C", now.Add(-time.Hour)}, {"HELD", now.Add(-time.Hour)}, {"FUTURE", now.Add(time.Hour)},
	} {
		require.NoError(t, db.Exec("INSERT INTO orders VALUES (?, 'completed', 'code')", item.no).Error)
		require.NoError(t, db.Exec("INSERT INTO supplier_settlements VALUES (?, 'frozen', ?)", item.no, item.due).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO aftersale_tickets VALUES ('HELD', 'open')").Error)
	repo := NewRepo(db)
	first, err := repo.ListDueSupplierOrders(context.Background(), now, nil, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, "Z", first[0].OrderNo)
	require.Equal(t, "A", first[1].OrderNo)
	// Keep the first page frozen to model failures; the cursor must still reach later rows.
	second, err := repo.ListDueSupplierOrders(context.Background(), now, &first[1], 2)
	require.NoError(t, err)
	require.Len(t, second, 2)
	require.Equal(t, "B", second[0].OrderNo)
	require.Equal(t, "C", second[1].OrderNo)
	last, err := repo.ListDueSupplierOrders(context.Background(), now, &second[1], 2)
	require.NoError(t, err)
	require.Empty(t, last)

	for _, mode := range []string{"code", "purchase"} {
		no := "Y-ACTIVE-" + mode
		require.NoError(t, db.Exec("INSERT INTO orders VALUES (?, 'active', ?)", no, mode).Error)
		require.NoError(t, db.Exec("INSERT INTO supplier_settlements VALUES (?, 'frozen', ?)", no, now.Add(-time.Hour)).Error)
	}
	active, err := repo.ListDueSupplierOrders(context.Background(), now, &second[1], 2)
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "Y-ACTIVE-purchase", active[0].OrderNo)
}
