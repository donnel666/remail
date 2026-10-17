package infra

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftDaumSupplierMetricsFollowImmutableAllocationOwner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.Exec(`
 CREATE TABLE email_resources(id INTEGER PRIMARY KEY,type TEXT,owner_user_id INTEGER);
 CREATE TABLE microsoft_allocations(order_no TEXT,resource_id INTEGER,supply_scope TEXT);
 CREATE TABLE daum_allocations(order_no TEXT,resource_id INTEGER,supply_scope TEXT);
 CREATE TABLE domain_allocations(order_no TEXT,resource_id INTEGER,supply_scope TEXT);
 CREATE TABLE allocation_order_guards(order_no TEXT,type TEXT,owner_user_id INTEGER);
 CREATE TABLE orders(id INTEGER PRIMARY KEY,order_no TEXT,status TEXT,service_mode TEXT,activated_at DATETIME,receive_until DATETIME,debit_tx_id INTEGER);
 CREATE TABLE mailmatch_order_delivery_heads(order_id INTEGER);
 INSERT INTO email_resources VALUES(1,'microsoft',3),(2,'daum',3);
 INSERT INTO orders VALUES
 (1,'MS-MAIN','completed','code',NULL,NULL,1),
 (2,'MS-ALIAS','completed','code',NULL,NULL,2),
 (3,'DAUM-MAIN','completed','code',NULL,NULL,3),
 (4,'DAUM-ALIAS','refunded','code',NULL,NULL,4),
 (5,'LEGACY-ALIAS','completed','code',NULL,NULL,5),
 (6,'MS-NULL','completed','code',NULL,NULL,6),
 (7,'DAUM-NULL','refunded','code',NULL,NULL,7);
 INSERT INTO mailmatch_order_delivery_heads VALUES(1),(2),(3),(5),(6);
 INSERT INTO microsoft_allocations VALUES('MS-MAIN',1,'public'),('MS-ALIAS',1,'public'),('LEGACY-ALIAS',1,'public'),('MS-NULL',1,'public');
 INSERT INTO daum_allocations VALUES('DAUM-MAIN',2,'public'),('DAUM-ALIAS',2,'public'),('DAUM-NULL',2,'public');
 INSERT INTO allocation_order_guards VALUES
 ('MS-MAIN','microsoft',3),('MS-ALIAS','microsoft',1),
 ('DAUM-MAIN','daum',3),('DAUM-ALIAS','daum',1),('LEGACY-ALIAS','microsoft',3),
 ('MS-NULL','microsoft',NULL),('DAUM-NULL','daum',NULL);
 `).Error)
	repo := NewBillingRepo(db)
	for _, newOwner := range []uint{3, 8} {
		require.NoError(t, db.Table("email_resources").Where("id IN ?", []uint{1, 2}).Update("owner_user_id", newOwner).Error)
		platform, err := repo.loadSupplierFulfillmentMetrics(context.Background(), db, 1)
		require.NoError(t, err)
		require.EqualValues(t, 2, platform.AllocationCount)
		require.Equal(t, 50.0, platform.FulfillmentSuccessRate)
		original, err := repo.loadSupplierFulfillmentMetrics(context.Background(), db, 3)
		require.NoError(t, err)
		if newOwner == 3 {
			require.EqualValues(t, 5, original.AllocationCount)
			require.Equal(t, 80.0, original.FulfillmentSuccessRate)
		} else {
			require.EqualValues(t, 3, original.AllocationCount, "non-NULL legacy alias income retains its original snapshot")
			require.Equal(t, 100.0, original.FulfillmentSuccessRate)
		}
		transferred, err := repo.loadSupplierFulfillmentMetrics(context.Background(), db, 8)
		require.NoError(t, err)
		if newOwner == 3 {
			require.Zero(t, transferred.AllocationCount)
		} else {
			require.EqualValues(t, 2, transferred.AllocationCount, "only pre-snapshot NULL rows retain the old root-owner statistics behavior")
			require.Equal(t, 50.0, transferred.FulfillmentSuccessRate)
		}
		var nullSnapshots int64
		require.NoError(t, db.Table("allocation_order_guards").Where("owner_user_id IS NULL").Count(&nullSnapshots).Error)
		require.EqualValues(t, 2, nullSnapshots, "statistics never backfill historical billing facts")
	}
}
