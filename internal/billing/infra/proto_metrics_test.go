package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/billing/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSupplierMetricsIncludeProtoPublicOrdersOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT, owner_user_id INTEGER);
CREATE TABLE microsoft_allocations (order_no TEXT, resource_id INTEGER, supply_scope TEXT);
CREATE TABLE domain_allocations (order_no TEXT, resource_id INTEGER, supply_scope TEXT);
CREATE TABLE proto_allocations (order_no TEXT, resource_id INTEGER, supply_scope TEXT);
CREATE TABLE orders (
    id INTEGER PRIMARY KEY, order_no TEXT, status TEXT, service_mode TEXT,
    activated_at DATETIME, receive_until DATETIME, debit_tx_id INTEGER
);
CREATE TABLE mailmatch_order_delivery_heads (order_id INTEGER);
INSERT INTO email_resources VALUES (1, 'proto', 7), (2, 'proto', 8);
INSERT INTO orders VALUES
    (1, 'PROTO-OK', 'completed', 'code', NULL, NULL, 1),
    (2, 'PROTO-FAILED', 'refunded', 'code', NULL, NULL, 2),
    (3, 'PROTO-PRIVATE', 'completed', 'code', NULL, NULL, 3),
    (4, 'HIST-PROTO', 'completed', 'code', NULL, NULL, 4),
    (5, 'PROTO-OTHER', 'completed', 'code', NULL, NULL, 5);
INSERT INTO mailmatch_order_delivery_heads VALUES (1), (3), (4), (5);
INSERT INTO proto_allocations VALUES
    ('PROTO-OK', 1, 'public'), ('PROTO-FAILED', 1, 'public'),
    ('PROTO-PRIVATE', 1, 'owned'), ('HIST-PROTO', 1, 'public'),
    ('PROTO-OTHER', 2, 'public');
`).Error)
	repo := NewBillingRepo(db)
	var summary domain.WalletSummary
	require.NoError(t, repo.populateSupplierFulfillmentMetrics(context.Background(), db, 7, &summary))
	require.EqualValues(t, 2, summary.SupplierAllocationCount)
	require.Equal(t, 50.0, summary.SupplierFulfillmentSuccessRate)
}
