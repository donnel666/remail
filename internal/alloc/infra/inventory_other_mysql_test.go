package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestICloudSharedSnapshotMixedProjectMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedICloudAllocationInventory(t, db, 2, 3)
	require.NoError(t, db.Exec(`UPDATE projects SET access_type='private' WHERE id=10`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(id,project_id,type,status,code_enabled,code_price,code_window_minutes,main_weight)
VALUES(21,10,'microsoft','enabled',TRUE,1,10,1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no,type) VALUES('icloud-snapshot-history','icloud')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_allocations(order_no,project_id,product_id,resource_id,alias_id,supply_scope,email,status)
SELECT 'icloud-snapshot-history',10,20,resource_id,id,'public',email,'released' FROM icloud_aliases WHERE resource_id=10000 ORDER BY id LIMIT 1`).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo, ctx := NewRepo(db, client), context.Background()
	warmICloudSnapshot(t, repo, server)
	for key, value := range map[string]any{
		microsoftSnapshotKey: microsoftSnapshotFixture(20, 30, 2, 3),
	} {
		payload, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, client.Set(ctx, key, payload, time.Hour).Err())
	}
	got, err := repo.GetFastProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	require.Equal(t, coredomain.ProductTypeICloud, got.Items[0].ProductType)
	require.EqualValues(t, 5, got.Items[0].PublicAvailable)
	require.EqualValues(t, 5, *got.Items[0].PurchaseAvailable)
	require.EqualValues(t, 50, got.TotalAvailable)
	require.NotNil(t, got.SourceRefreshedAt)

	// Active orders are allocation facts even after their resources leave the public pool.
	require.NoError(t, db.Exec(`UPDATE icloud_resources SET for_sale=FALSE WHERE id=10000`).Error)
	require.NoError(t, db.Exec(`UPDATE icloud_resources SET status='disabled' WHERE id=10001`).Error)
	require.NoError(t, db.Exec(`UPDATE icloud_allocations SET status='allocated'`).Error)
	require.NoError(t, db.Exec(`INSERT INTO allocation_order_guards(order_no,type) VALUES('icloud-disabled-active','icloud')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_allocations(order_no,project_id,product_id,resource_id,alias_id,supply_scope,email,status)
SELECT 'icloud-disabled-active',10,20,resource_id,id,'public',email,'allocated' FROM icloud_aliases WHERE resource_id=10001 ORDER BY id LIMIT 1`).Error)
	require.NoError(t, client.Del(ctx, iCloudInventorySnapshotKey).Err())
	snapshot := warmICloudSnapshot(t, repo, server)
	require.Zero(t, snapshot.Aliases)
	require.Zero(t, snapshot.Used[10])
	require.EqualValues(t, 2, snapshot.Active[10])
}
