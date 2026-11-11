package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	core "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func microsoftSnapshotFixture(main, aliases, mainUsed, aliasUsed int64) microsoftInventorySnapshot {
	now := time.Now().UTC()
	return microsoftInventorySnapshot{Generation: "fixture", StartedAt: now, ComputedAt: now, Main: map[string]int64{"outlook.com": main}, Aliases: map[string]int64{"outlook.com": aliases}, Used: map[uint]*microsoftSnapshotUsage{10: {Main: map[string]int64{"outlook.com": mainUsed}, Aliases: map[string]int64{"outlook.com": aliasUsed}}}, Active: map[uint]int64{}}
}

func microsoftSnapshotTestRepo(t *testing.T) (*Repo, *miniredis.Miniredis) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	for _, sql := range []string{
		`CREATE TABLE projects(id INTEGER PRIMARY KEY,status TEXT)`,
		`CREATE TABLE project_products(id INTEGER PRIMARY KEY,project_id INTEGER,type TEXT,status TEXT,code_enabled BOOLEAN,purchase_enabled BOOLEAN,main_weight INTEGER,dot_weight INTEGER,plus_weight INTEGER)`,
		`CREATE TABLE users(id INTEGER PRIMARY KEY,status TEXT,role TEXT)`,
		`CREATE TABLE email_resources(id INTEGER PRIMARY KEY,type TEXT,owner_user_id INTEGER)`,
		`CREATE TABLE microsoft_resources(id INTEGER PRIMARY KEY,email_domain TEXT,status TEXT,for_sale BOOLEAN,token_refresh_status TEXT)`,
		`CREATE TABLE explicit_aliases(id INTEGER PRIMARY KEY,resource_id INTEGER,email_domain TEXT,status TEXT)`,
		`CREATE TABLE microsoft_allocations(id INTEGER PRIMARY KEY,resource_id INTEGER,explicit_alias_id INTEGER,project_id INTEGER,mailbox TEXT,status TEXT)`,
		`CREATE TABLE mailmatch_resource_fetch_states(email_resource_id INTEGER,operation_kind TEXT,status TEXT)`,
		`CREATE TABLE microsoft_resource_project_matches(resource_id INTEGER,project_id INTEGER,PRIMARY KEY(resource_id,project_id))`,
		`CREATE TABLE project_microsoft_suffix_blacklists(project_id INTEGER,suffix TEXT)`,
		`INSERT INTO projects VALUES(10,'listed'),(11,'listed')`,
		`INSERT INTO project_products VALUES(20,10,'microsoft','enabled',1,1,1,0,0),(21,11,'microsoft','enabled',1,1,1,0,0)`,
		`INSERT INTO users VALUES(1,'active','admin'),(2,'disabled','supplier'),(3,'active','user'),(4,'active','supplier')`,
		`INSERT INTO email_resources VALUES(1,'microsoft',1),(2,'microsoft',1),(3,'microsoft',1),(4,'microsoft',2),(5,'microsoft',1),(6,'microsoft',4),(7,'microsoft',1),(8,'microsoft',1),(9,'microsoft',1),(10,'microsoft',1)`,
		`INSERT INTO microsoft_resources VALUES(1,'outlook.com','normal',1,'idle'),(2,'outlook.com','normal',0,'idle'),(3,'outlook.com','pending',1,'idle'),(4,'hotmail.com','normal',1,'idle'),(5,'outlook.com','normal',1,'pending'),(6,'outlook.com','normal',1,'idle'),(7,'outlook.com','normal',1,'idle'),(8,'hotmail.com','normal',1,'idle'),(9,'outlook.com','normal',1,'idle'),(10,'tenant.onmicrosoft.com','normal',1,'idle')`,
		`INSERT INTO explicit_aliases VALUES(11,1,'outlook.com','normal'),(12,2,'outlook.com','normal'),(13,3,'outlook.com','normal'),(14,6,'hotmail.com','normal'),(15,7,'outlook.com','disabled'),(16,7,'outlook.com','normal'),(17,8,'hotmail.com','normal'),(18,6,'outlook.com','normal')`,
		`INSERT INTO microsoft_allocations VALUES(1,1,NULL,10,'main','released'),(2,1,NULL,10,'main','allocated'),(3,2,NULL,10,'main','released'),(4,3,NULL,10,'main','released'),(5,1,11,10,'alias','released'),(6,1,11,10,'alias','allocated'),(7,2,12,10,'alias','allocated'),(8,6,14,11,'alias','released'),(9,6,NULL,11,'main','allocated')`,
		`INSERT INTO microsoft_resource_project_matches VALUES(1,10),(7,10)`,
		`INSERT INTO mailmatch_resource_fetch_states VALUES(9,'resource_history','pending')`,
		`INSERT INTO project_microsoft_suffix_blacklists VALUES(10,'hotmail.com')`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	return NewRepo(db, client), server
}

func finishMicrosoftSnapshot(t *testing.T, r *Repo, server *miniredis.Miniredis) *microsoftInventorySnapshot {
	t.Helper()
	for range 50 {
		server.FastForward(time.Second)
		v, err := r.advanceMicrosoftInventory(context.Background())
		if err == nil {
			return v
		}
		require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	}
	t.Fatal("snapshot did not finish")
	return nil
}
func expireMicrosoftSnapshot(t *testing.T, r *Repo) {
	t.Helper()
	ctx := context.Background()
	data, err := r.redis.Get(ctx, microsoftSnapshotKey).Bytes()
	require.NoError(t, err)
	var v microsoftInventorySnapshot
	require.NoError(t, json.Unmarshal(data, &v))
	v.ComputedAt = time.Now().Add(-2 * time.Hour)
	data, err = json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, r.redis.Set(ctx, microsoftSnapshotKey, data, 7*24*time.Hour).Err())
}
func TestMicrosoftSnapshotCountsSamePublicScopeAndHistoricalUsage(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	s := finishMicrosoftSnapshot(t, r, server)
	require.EqualValues(t, 4, sumInventorySuffixes(s.Main))
	require.EqualValues(t, 5, sumInventorySuffixes(s.Aliases))
	items, main, aliases, err := s.available(10, nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, main)
	require.EqualValues(t, 3, aliases)
	require.Len(t, items, 2)
	require.EqualValues(t, 3, s.Active[10], "active private orders must remain visible")
	totals, err := r.GetFastProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, totals.TotalAvailable, "project blacklist applies to main and alias suffixes")
	require.Len(t, totals.Items, 1)
	require.Len(t, totals.Items[0].Suffixes, 1)
	require.EqualValues(t, 2, totals.Items[0].Suffixes[0].TotalAvailable)
	require.True(t, s.StartedAt.Equal(totals.SourceRefreshedAt[core.ProductTypeMicrosoft]))
}
func TestMicrosoftPeriodicRebuildSeesStateChangesWithoutNotifications(t *testing.T) {
	for _, tc := range []struct {
		name, sql     string
		main, aliases int64
	}{
		{"validation succeeds", `UPDATE microsoft_resources SET status='normal' WHERE id=3`, 2, 4},
		{"private becomes public", `UPDATE microsoft_resources SET for_sale=1 WHERE id=2`, 2, 3},
		{"public becomes private", `UPDATE microsoft_resources SET for_sale=0 WHERE id=6`, 1, 1},
		{"validation fails", `UPDATE microsoft_resources SET status='abnormal' WHERE id=6`, 1, 1},
		{"supplier disabled", `UPDATE users SET status='disabled' WHERE id=4`, 1, 1},
		{"alias disabled", `UPDATE explicit_aliases SET status='disabled' WHERE id=18`, 2, 2},
		{"late old history", `INSERT INTO microsoft_allocations VALUES(100,6,18,10,'alias','released')`, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, server := microsoftSnapshotTestRepo(t)
			old := finishMicrosoftSnapshot(t, r, server)
			require.NoError(t, r.db.Exec(tc.sql).Error)
			expireMicrosoftSnapshot(t, r)
			updated := finishMicrosoftSnapshot(t, r, server)
			require.NotEqual(t, old.Generation, updated.Generation)
			_, main, aliases, err := updated.available(10, nil)
			require.NoError(t, err)
			require.Equal(t, tc.main, main)
			require.Equal(t, tc.aliases, aliases)
		})
	}
}
func TestMicrosoftColdBuildRetainsUsableProjectSnapshotAndNeverPublishesPartialPool(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour)
	cache := NewInventoryCache(r.redis)
	require.NoError(t, cache.SetInventoryStats(ctx, 10, &allocapp.InventoryStats{ProjectID: 10, Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true, MainAvailable: 40, TotalAvailable: 47}}, 7*24*time.Hour))
	require.NoError(t, cache.SetProductInventoryTotals(ctx, 10, &allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 47, SourceRefreshedAt: map[core.ProductType]time.Time{core.ProductTypeMicrosoft: old}, Items: []allocapp.ProductInventoryTotal{{ProductID: 20, ProductType: core.ProductTypeMicrosoft, TotalAvailable: 47, PublicAvailable: 47}}}, 7*24*time.Hour))
	got, err := r.GetFastProductInventoryTotals(ctx, 10)
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.True(t, got.RefreshPending)
	require.EqualValues(t, 47, got.TotalAvailable)
	require.True(t, old.Equal(got.SourceRefreshedAt[core.ProductTypeMicrosoft]))
	require.False(t, server.Exists(microsoftSnapshotKey))
	ready := finishMicrosoftSnapshot(t, r, server)
	require.NotNil(t, ready)
	got, err = r.GetFastProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, got.TotalAvailable)
}
func TestMicrosoftSnapshotProgressSurvivesChangesAndLongBuilds(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	_, err := r.advanceMicrosoftInventory(ctx)
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	data, err := r.redis.Get(ctx, microsoftSnapshotKey+":build").Bytes()
	require.NoError(t, err)
	var state microsoftSnapshotBuild
	require.NoError(t, json.Unmarshal(data, &state))
	require.EqualValues(t, 10, state.Cursor)
	state.StartedAt = time.Now().Add(-3 * time.Hour)
	data, err = json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, r.redis.Set(ctx, microsoftSnapshotKey+":build", data, 24*time.Hour).Err())
	require.NoError(t, r.db.Exec(`UPDATE microsoft_resources SET status='normal' WHERE id=3`).Error)
	result := finishMicrosoftSnapshot(t, r, server)
	require.Equal(t, state.Generation, result.Generation)
	require.EqualValues(t, 4, sumInventorySuffixes(result.Main), "already-scanned pool pages are not restarted on every change")
	expireMicrosoftSnapshot(t, r)
	next := finishMicrosoftSnapshot(t, r, server)
	require.EqualValues(t, 5, sumInventorySuffixes(next.Main), "the following generation converges to changed old rows")
}
func TestMicrosoftSnapshotRejectsInconsistentUsageInsteadOfZero(t *testing.T) {
	s := microsoftSnapshotFixture(1, 2, 2, 0)
	_, _, _, err := s.available(10, nil)
	require.ErrorContains(t, err, "usage exceeds pool")
}

func TestMicrosoftSnapshotExpiredSourceIsNotRenewedByAnotherProvider(t *testing.T) {
	r, _ := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	cache := NewInventoryCache(r.redis)
	old := time.Now().Add(-8 * 24 * time.Hour)
	now := time.Now()
	require.NoError(t, cache.RefreshProductInventoryTotals(ctx, 10, &allocapp.ProjectProductInventoryTotals{ProjectID: 10, SourceRefreshedAt: map[core.ProductType]time.Time{core.ProductTypeMicrosoft: old, core.ProductTypeICloud: now}, TotalAvailable: 120, Items: []allocapp.ProductInventoryTotal{{ProductID: 20, ProductType: core.ProductTypeMicrosoft, TotalAvailable: 100}, {ProductID: 30, ProductType: core.ProductTypeICloud, TotalAvailable: 20}}}, 7*24*time.Hour))
	got, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, core.ProductTypeICloud, got.Items[0].ProductType)
	require.EqualValues(t, 20, got.TotalAvailable)
}
func TestMicrosoftSnapshotLeaseLossCannotCommitPage(t *testing.T) {
	r, _ := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	lock, key := microsoftSnapshotKey+":lock", microsoftSnapshotKey+":build"
	require.NoError(t, r.redis.Set(ctx, lock, "new-worker", 10*time.Second).Err())
	state := &microsoftSnapshotBuild{microsoftInventorySnapshot: microsoftSnapshotFixture(30, 40, 0, 0), Cursor: 20}
	written, err := r.storeMicrosoftInventoryPage(ctx, lock, key, "expired-worker", state, true)
	require.NoError(t, err)
	require.False(t, written)
	require.Zero(t, r.redis.Exists(ctx, key, microsoftSnapshotKey).Val())
}
func TestMicrosoftScopeSnapshotKeepsOtherInventoryWhenMicrosoftFails(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	cache := NewInventoryCache(r.redis)
	require.NoError(t, r.db.Exec(`INSERT INTO project_products VALUES(30,10,'icloud','enabled',0,1,1,0,0)`).Error)
	_, args := iCloudForwardingDomainCondition("ia.forward_to_email")
	forwarding, _ := json.Marshal(args)
	data, err := json.Marshal(iCloudInventorySnapshot{ScopePaired: true, ComputedAt: time.Now().UTC(), Forwarding: string(forwarding), Aliases: 123, Used: map[uint]int64{}, Active: map[uint]int64{}})
	require.NoError(t, err)
	require.NoError(t, r.redis.Set(ctx, iCloudInventorySnapshotKey, data, time.Hour).Err())
	require.NoError(t, r.db.Exec(`DROP TABLE microsoft_resources`).Error)
	totals, err := r.GetFastProductInventoryTotals(ctx, 10)
	require.Error(t, err)
	require.NotNil(t, totals)
	require.True(t, totals.RefreshPending)
	require.Len(t, totals.Items, 1)
	require.Equal(t, core.ProductTypeICloud, totals.Items[0].ProductType)
	require.EqualValues(t, 123, totals.TotalAvailable)
	require.NoError(t, cache.RefreshProductInventoryTotals(ctx, 10, totals, 7*24*time.Hour))
	server.FastForward(time.Second)
	got, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 123, got.TotalAvailable)
}

func TestMicrosoftManualRefreshPreservesProgressAndKeepsNewerRequest(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	first := finishMicrosoftSnapshot(t, r, server)
	require.NoError(t, r.db.Exec(`UPDATE microsoft_resources SET for_sale=TRUE WHERE id=2`).Error)
	require.NoError(t, r.RequestInventorySnapshotRebuild(ctx, nil))
	request1 := r.redis.Get(ctx, microsoftSnapshotKey+":request").Val()
	server.FastForward(time.Second)
	served, err := r.sharedMicrosoftInventory(ctx)
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.Equal(t, first.Generation, served.Generation)
	data, err := r.redis.Get(ctx, microsoftSnapshotKey+":build").Bytes()
	require.NoError(t, err)
	var build microsoftSnapshotBuild
	require.NoError(t, json.Unmarshal(data, &build))
	require.NoError(t, r.db.Exec(`UPDATE microsoft_resources SET status='normal' WHERE id=3`).Error)
	require.NoError(t, r.RequestInventorySnapshotRebuild(ctx, nil))
	request2 := r.redis.Get(ctx, microsoftSnapshotKey+":request").Val()
	require.NotEqual(t, request1, request2)
	second := finishMicrosoftSnapshot(t, r, server)
	require.Equal(t, build.Generation, second.Generation)
	require.Equal(t, request1, second.RequestID)
	require.EqualValues(t, 5, sumInventorySuffixes(second.Main))
	third := finishMicrosoftSnapshot(t, r, server)
	require.NotEqual(t, second.Generation, third.Generation)
	require.Equal(t, request2, third.RequestID)
	require.EqualValues(t, 6, sumInventorySuffixes(third.Main))
}
func TestQuietScheduleDoesNotRebuildButManualRequestWakesSnapshot(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	finishMicrosoftSnapshot(t, r, server)
	expireMicrosoftSnapshot(t, r)
	require.NoError(t, r.WarmFastInventoryBase(ctx))
	require.False(t, server.Exists(microsoftSnapshotKey+":build"))
	require.NoError(t, r.RequestInventorySnapshotRebuild(ctx, nil))
	server.FastForward(time.Second)
	require.ErrorIs(t, r.WarmFastInventoryBase(ctx), platform.ErrBackgroundExecutionDeferred)
	require.True(t, server.Exists(microsoftSnapshotKey+":build"))
}

func TestMicrosoftDenseHistoryShrinksPageWithoutCommittingPartialCounts(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	for _, query := range []string{
		`WITH RECURSIVE n(id) AS (SELECT 100 UNION ALL SELECT id+1 FROM n WHERE id<5099) INSERT INTO email_resources SELECT id,'microsoft',1 FROM n`,
		`INSERT INTO microsoft_resources SELECT id,'outlook.com','normal',1,'idle' FROM email_resources WHERE id>=100`,
		`WITH RECURSIVE n(id) AS (SELECT 100 UNION ALL SELECT id+1 FROM n WHERE id<5099), p(id) AS (SELECT 100 UNION ALL SELECT id+1 FROM p WHERE id<104) INSERT INTO microsoft_allocations(resource_id,project_id,mailbox,status) SELECT n.id,p.id,'main','released' FROM n CROSS JOIN p`,
	} {
		require.NoError(t, r.db.Exec(query).Error)
	}
	_, err := r.advanceMicrosoftInventory(ctx)
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	raw, err := r.redis.Get(ctx, microsoftSnapshotKey+":build").Bytes()
	require.NoError(t, err)
	var state microsoftSnapshotBuild
	require.NoError(t, json.Unmarshal(raw, &state))
	require.Equal(t, 2500, state.PageSize)
	require.Zero(t, state.Cursor)
	require.Empty(t, state.Main)
	require.Empty(t, state.Used)
	completed := finishMicrosoftSnapshot(t, r, server)
	require.EqualValues(t, 5004, sumInventorySuffixes(completed.Main))
	require.EqualValues(t, 5000, completed.Used[100].Main["outlook.com"])
	_, main, aliases, err := completed.available(100, nil)
	require.NoError(t, err)
	require.EqualValues(t, 4, main)
	require.EqualValues(t, 5, aliases)
}
