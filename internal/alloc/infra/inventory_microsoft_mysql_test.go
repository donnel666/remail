package infra

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMicrosoftSnapshotMatchesAuthoritativeInventoryMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 6, true, "normal")
	for _, query := range []string{
		`UPDATE microsoft_resources SET for_sale=FALSE WHERE id=1001`,
		`UPDATE microsoft_resources SET status='abnormal' WHERE id=1002`,
		`INSERT INTO explicit_aliases(id,resource_id,owner_user_id,email,status) VALUES(201,1000,1,'free@outlook.com','normal'),(202,1001,1,'private@outlook.com','normal'),(203,1003,1,'used@outlook.com','normal'),(204,1004,1,'blocked@outlook.com','normal'),(205,1005,1,'invalid@outlook.com','disabled')`,
		`INSERT INTO allocation_order_guards(order_no,type) VALUES('snapshot-main','microsoft'),('snapshot-main-repeat','microsoft'),('snapshot-private','microsoft'),('snapshot-alias','microsoft')`,
		`INSERT INTO microsoft_allocations(order_no,project_id,product_id,resource_id,mailbox,supply_scope,email,status) VALUES('snapshot-main',10,20,1003,'main','public','main-1003@outlook.com','released'),('snapshot-main-repeat',10,20,1003,'main','public','main-1003@outlook.com','released'),('snapshot-private',10,20,1001,'main','owned','main-1001@outlook.com','released')`,
		`INSERT INTO microsoft_allocations(order_no,project_id,product_id,resource_id,mailbox,explicit_alias_id,supply_scope,email,status) VALUES('snapshot-alias',10,20,1003,'alias',203,'public','used@outlook.com','released')`,
		`INSERT INTO microsoft_resource_project_matches(resource_id,project_id,first_matched_at,last_matched_at,last_scanned_at) VALUES(1004,10,NOW(3),NOW(3),NOW(3))`,
	} {
		require.NoError(t, db.Exec(query).Error)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo := NewRepo(db, client)
	ctx := context.Background()
	assertMatches := func() {
		t.Helper()
		finishMicrosoftSnapshot(t, repo, server)
		actual, err := repo.GetFastProductInventoryTotals(ctx, 10)
		require.NoError(t, err)
		expected, err := repo.GetProductInventoryTotals(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, expected.TotalAvailable, actual.TotalAvailable)
		require.Equal(t, expected.Items[0].Suffixes, actual.Items[0].Suffixes)
	}
	assertMatches()
	require.NoError(t, db.Exec(`UPDATE microsoft_resources SET for_sale=TRUE,status='normal' WHERE id IN (1001,1002)`).Error)
	expireMicrosoftSnapshot(t, repo)
	assertMatches()
	require.NoError(t, db.Exec(`UPDATE microsoft_resources SET for_sale=FALSE WHERE id=1000`).Error)
	expireMicrosoftSnapshot(t, repo)
	assertMatches()
}

type inventoryScopeBarrier struct {
	logger.Interface
	once             sync.Once
	observed, resume chan struct{}
}

func (b *inventoryScopeBarrier) Trace(ctx context.Context, _ time.Time, fc func() (string, int64), _ error) {
	query, _ := fc()
	if strings.Contains(query, "ms.id, ms.email_domain AS suffix") {
		b.once.Do(func() {
			close(b.observed)
			select {
			case <-b.resume:
			case <-ctx.Done():
			}
		})
	}
}
func TestMicrosoftInventoryPageDoesNotBlockWritesAndUsesOneReadViewMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	barrier := &inventoryScopeBarrier{Interface: logger.Default.LogMode(logger.Silent), observed: make(chan struct{}), resume: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	repo := NewRepo(db.Session(&gorm.Session{Logger: barrier}))
	state := &microsoftSnapshotBuild{Phase: 0, Cursor: 999, Upper: 1000, PageSize: 5000}
	type result struct {
		eligible []microsoftSnapshotResource
		used     []microsoftSnapshotUse
		err      error
	}
	done := make(chan result, 1)
	go func() { _, _, e, u, _, err := repo.readMicrosoftSnapshotPage(ctx, state); done <- result{e, u, err} }()
	select {
	case <-barrier.observed:
	case <-ctx.Done():
		t.Fatal("eligible pool query did not finish")
	}
	defer close(barrier.resume)
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer writeCancel()
	require.NoError(t, db.WithContext(writeCtx).Exec(`UPDATE microsoft_resources SET for_sale=FALSE WHERE id=1000`).Error, "inventory reads must not lock resource publication")
	require.NoError(t, db.WithContext(writeCtx).Exec(`INSERT INTO allocation_order_guards(order_no,type) VALUES('concurrent-inventory-use','microsoft')`).Error)
	require.NoError(t, db.WithContext(writeCtx).Exec(`INSERT INTO microsoft_allocations(order_no,project_id,product_id,resource_id,mailbox,supply_scope,email,status) VALUES('concurrent-inventory-use',10,20,1000,'main','public','main@outlook.com','released')`).Error)
	// Releasing the read barrier via cancellation would cancel the query; signal with a value instead.
	select {
	case barrier.resume <- struct{}{}:
	case <-ctx.Done():
		t.Fatal("page barrier deadline")
	}
	got := <-done
	require.NoError(t, got.err)
	require.Len(t, got.eligible, 1)
	require.Empty(t, got.used, "history committed after the page read-view must not be subtracted from that page")
	require.EqualValues(t, 1000, got.eligible[0].ID)
}
