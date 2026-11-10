package infra

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func iCloudSnapshotTestRepo(t *testing.T) (*Repo, *miniredis.Miniredis) {
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
	old := runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, "")
	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, "forward.test")
	t.Cleanup(func() { runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, old) })
	for _, query := range []string{
		`CREATE TABLE projects(id INTEGER PRIMARY KEY,status TEXT,access_type TEXT)`,
		`CREATE TABLE project_products(id INTEGER PRIMARY KEY,project_id INTEGER,type TEXT,status TEXT,code_enabled BOOLEAN,purchase_enabled BOOLEAN,main_weight INTEGER,dot_weight INTEGER,plus_weight INTEGER)`,
		`CREATE TABLE email_resources(id INTEGER PRIMARY KEY,type TEXT,owner_user_id INTEGER)`,
		`CREATE TABLE icloud_resources(id INTEGER PRIMARY KEY,status TEXT,for_sale BOOLEAN)`,
		`CREATE TABLE icloud_aliases(id INTEGER PRIMARY KEY,resource_id INTEGER,status TEXT,forward_to_email TEXT)`,
		`CREATE TABLE icloud_allocations(id INTEGER PRIMARY KEY,alias_id INTEGER,project_id INTEGER,status TEXT,UNIQUE(alias_id,project_id))`,
		`CREATE TABLE domain_resources(id INTEGER PRIMARY KEY,domain TEXT,purpose TEXT,status TEXT)`,
		`INSERT INTO projects VALUES(10,'listed','private'),(11,'listed','public')`,
		`INSERT INTO project_products VALUES(20,10,'icloud','enabled',1,1,0,0,0),(21,11,'icloud','enabled',1,1,0,0,0)`,
		`INSERT INTO email_resources VALUES(1,'icloud',1),(2,'icloud',1),(3,'icloud',2)`,
		`INSERT INTO icloud_resources VALUES(1,'normal',1),(2,'normal',0),(3,'disabled',1)`,
		`INSERT INTO domain_resources VALUES(1,'forward.test','binding','normal')`,
		`INSERT INTO icloud_aliases VALUES(1,1,'normal','x@forward.test'),(2,1,'normal','x@forward.test'),(3,1,'normal','x@forward.test'),(4,2,'normal','x@forward.test'),(5,3,'normal','x@forward.test'),(6,1,'disabled','x@forward.test'),(7,1,'normal','x@unconfigured.test')`,
		`INSERT INTO icloud_allocations VALUES(1,1,10,'released'),(2,2,10,'allocated'),(3,3,11,'released')`,
	} {
		require.NoError(t, db.Exec(query).Error)
	}
	return NewRepo(db, client), server
}

func warmICloudSnapshot(t *testing.T, repo *Repo, server *miniredis.Miniredis) *iCloudInventorySnapshot {
	t.Helper()
	for range 12 {
		server.FastForward(time.Second)
		snapshot, err := repo.sharedICloudInventory(context.Background())
		if err == nil {
			return snapshot
		}
		require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	}
	t.Fatal("snapshot did not finish")
	return nil
}

func TestICloudSharedSnapshotCountsHistoryAndSupportsPrivateProjects(t *testing.T) {
	repo, server := iCloudSnapshotTestRepo(t)
	ctx := context.Background()
	condition, args := iCloudForwardingDomainCondition("ia.forward_to_email")
	config, _ := json.Marshal(args)
	_, err := repo.advanceICloudInventory(ctx, condition, args, string(config))
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.False(t, server.Exists(iCloudInventorySnapshotKey), "a partial page must not become a completed snapshot")
	snapshot := warmICloudSnapshot(t, repo, server)
	require.EqualValues(t, 3, snapshot.Aliases)
	require.EqualValues(t, 2, snapshot.Used[10], "released and active histories both consume this project's aliases")
	require.EqualValues(t, 1, snapshot.Used[11])
	for _, id := range []uint{10, 11} {
		server.FastForward(time.Second)
		totals, err := repo.GetFastProductInventoryTotals(ctx, id)
		require.NoError(t, err, "iCloud-only projects must not need Microsoft tables or a Microsoft baseline")
		require.False(t, totals.Cold)
		require.Len(t, totals.Items, 1)
		require.NotNil(t, totals.SourceRefreshedAt)
		require.EqualValues(t, id-9, totals.TotalAvailable)
		require.Equal(t, totals.TotalAvailable, *totals.Items[0].PurchaseAvailable)
	}
}

func TestICloudSnapshotCursorDoesNotRescanOrPublishPartialResults(t *testing.T) {
	repo, server := iCloudSnapshotTestRepo(t)
	require.NoError(t, repo.db.Exec(`WITH RECURSIVE n(id) AS (SELECT 8 UNION ALL SELECT id+1 FROM n WHERE id<5010)
INSERT INTO icloud_aliases SELECT id,1,'normal','x@forward.test' FROM n`).Error)
	condition, args := iCloudForwardingDomainCondition("ia.forward_to_email")
	config, _ := json.Marshal(args)
	_, err := repo.advanceICloudInventory(context.Background(), condition, args, string(config))
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	var state iCloudInventoryBuild
	data, err := repo.redis.Get(context.Background(), iCloudInventorySnapshotKey+":build").Bytes()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &state))
	require.EqualValues(t, 5000, state.AliasCursor)
	require.False(t, server.Exists(iCloudInventorySnapshotKey))
	_, err = repo.advanceICloudInventory(context.Background(), condition, args, string(config))
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred, "all workers share the scan budget")
	snapshot := warmICloudSnapshot(t, repo, server)
	require.EqualValues(t, 5006, snapshot.Aliases)
	require.EqualValues(t, 2, snapshot.Used[10])
	require.False(t, server.Exists(iCloudInventorySnapshotKey+":build"))
}

func TestICloudSnapshotPreservesLastGoodValueWhileRebuilding(t *testing.T) {
	repo, server := iCloudSnapshotTestRepo(t)
	snapshot := warmICloudSnapshot(t, repo, server)
	snapshot.ComputedAt = time.Now().Add(-2 * time.Hour)
	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, repo.redis.Set(context.Background(), iCloudInventorySnapshotKey, payload, 7*24*time.Hour).Err())
	server.FastForward(time.Second)
	got, err := repo.sharedICloudInventory(context.Background())
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.True(t, snapshot.ComputedAt.Equal(got.ComputedAt))
	require.Equal(t, snapshot.Aliases, got.Aliases)
	require.NoError(t, repo.db.Exec("DROP TABLE icloud_allocations").Error)
	server.FastForward(time.Second)
	_, _ = repo.sharedICloudInventory(context.Background())
	server.FastForward(time.Second)
	_, err = repo.sharedICloudInventory(context.Background())
	require.Error(t, err)
	if errors.Is(err, platform.ErrBackgroundExecutionDeferred) {
		t.Fatal(err)
	}
	// The previously completed Redis value survives a failed rebuild.
	raw, readErr := repo.redis.Get(context.Background(), iCloudInventorySnapshotKey).Bytes()
	require.NoError(t, readErr)
	require.JSONEq(t, string(payload), string(raw))
}

func TestUnknownNonMicrosoftZeroIsOmittedAndRepaired(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ctx := NewInventoryCache(client), context.Background()
	old := &allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 107, Items: []allocapp.ProductInventoryTotal{
		{ProductID: 1, ProductType: coredomain.ProductTypeMicrosoft, TotalAvailable: 100, PublicAvailable: 100},
		{ProductID: 2, ProductType: coredomain.ProductTypeICloud},
		{ProductID: 3, ProductType: coredomain.ProductTypeGmail, TotalAvailable: 7, PublicAvailable: 7},
	}}
	require.NoError(t, cache.SetProductInventoryTotals(ctx, 10, old, 7*24*time.Hour))
	key := inventoryCacheKey(allocapp.InventoryCacheProducts, 10)
	payload, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	require.NoError(t, client.ZAdd(ctx, inventoryCacheScheduleKey, redis.Z{Score: float64(time.Now().Add(7 * 24 * time.Hour).UnixMilli()), Member: key}).Err())
	totals, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Len(t, totals.Items, 2)
	require.EqualValues(t, 107, totals.TotalAvailable)
	require.LessOrEqual(t, client.ZScore(ctx, inventoryCacheScheduleKey, key).Val(), float64(time.Now().UnixMilli()))
	require.Equal(t, payload, client.Get(ctx, key).Val(), "reads must not rewrite old snapshots")
	useCase := allocapp.NewUseCase(&inventoryCacheRepoStub{})
	useCase.SetInventoryCache(cache)
	available, err := useCase.HasProductInventory(ctx, allocapp.ProductInventoryAvailabilityRequest{ProjectID: 10, ProductID: 2, PublicOnly: true})
	require.NoError(t, err)
	require.True(t, available, "unknown iCloud stock must defer to the allocator instead of rejecting public orders")
	stamp := time.Now().UTC()
	old.SourceRefreshedAt = map[coredomain.ProductType]time.Time{coredomain.ProductTypeICloud: stamp, coredomain.ProductTypeGmail: stamp}
	require.NoError(t, cache.SetProductInventoryTotals(ctx, 10, old, 7*24*time.Hour))
	totals, err = cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Len(t, totals.Items, 3, "a measured zero must remain distinguishable from unknown")
}

func TestInventoryNormalizationRepairsColdAndLegacySnapshotsSoon(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ctx := NewInventoryCache(client), context.Background()
	now := time.Now().UTC()
	for _, id := range []uint{95, 220} {
		snapshot := &allocapp.ProjectProductInventoryTotals{ProjectID: id, Cold: id == 95}
		if id == 220 {
			snapshot.Items = []allocapp.ProductInventoryTotal{{ProductID: 1, ProductType: coredomain.ProductTypeICloud}}
		}
		require.NoError(t, cache.SetProductInventoryTotals(ctx, id, snapshot, 7*24*time.Hour))
		for _, kind := range []allocapp.InventoryCacheKind{allocapp.InventoryCacheStats, allocapp.InventoryCacheProducts} {
			require.NoError(t, client.ZAdd(ctx, inventoryCacheScheduleKey, redis.Z{Score: float64(now.Add(7 * 24 * time.Hour).UnixMilli()), Member: inventoryCacheKey(kind, id)}).Err())
		}
	}
	require.NoError(t, cache.NormalizeInventoryRefreshSchedule(ctx, now, []uint{95, 220}))
	for _, id := range []uint{95, 220} {
		for _, kind := range []allocapp.InventoryCacheKind{allocapp.InventoryCacheStats, allocapp.InventoryCacheProducts} {
			score, err := client.ZScore(ctx, inventoryCacheScheduleKey, inventoryCacheKey(kind, id)).Result()
			require.NoError(t, err)
			require.GreaterOrEqual(t, score, float64(now.UnixMilli()))
			require.Less(t, score, float64(now.Add(time.Minute).UnixMilli()))
		}
	}
}

func TestFastMixedInventoryComputesEveryEnabledProduct(t *testing.T) {
	db := newProtoSuffixInventoryDB(t)
	for _, query := range []string{
		`INSERT INTO project_products VALUES(21,10,'icloud','enabled',1,1,0,0,0),(22,10,'gmail','enabled',1,1,1,0,0),(23,10,'gmail_variant','enabled',1,1,0,0,1),(24,10,'domain','enabled',1,1,0,0,0),(25,10,'daum','enabled',1,1,1,0,0),(26,10,'microsoft_enterprise','enabled',1,1,1,0,0),(27,10,'microsoft','enabled',1,1,1,0,0)`,
		`INSERT INTO email_resources VALUES(100,'domain',1),(101,'daum',1),(102,'microsoft_enterprise',1),(103,'microsoft_enterprise',1),(104,'gmail',1),(105,'gmail',2)`,
		`CREATE TABLE allocation_daily_usages(resource_id INTEGER,usage_date TEXT,resource_type TEXT,usage_kind TEXT,used_count INTEGER)`,
		`CREATE TABLE domain_resources(id INTEGER PRIMARY KEY,domain TEXT,domain_tld TEXT,purpose TEXT,status TEXT,mail_server_id INTEGER,mailbox_daily_limit INTEGER)`,
		`CREATE TABLE mail_servers(id INTEGER PRIMARY KEY,status TEXT)`,
		`INSERT INTO mail_servers VALUES(1,'online')`,
		`INSERT INTO domain_resources VALUES(100,'example.com','com','sale','normal',1,20)`,
		`CREATE TABLE gmail_resources(id INTEGER PRIMARY KEY,status TEXT,for_sale BOOLEAN,email TEXT)`,
		`INSERT INTO gmail_resources VALUES(104,'normal',1,'ab@gmail.com'),(105,'normal',0,'private@gmail.com')`,
		`DROP TABLE gmail_allocations`,
		`CREATE TABLE gmail_allocations(resource_id INTEGER,project_id INTEGER,status TEXT,source TEXT,mailbox TEXT)`,
		`CREATE TABLE microsoft_enterprise_resources(id INTEGER PRIMARY KEY,status TEXT,for_sale BOOLEAN,email_domain TEXT,token_refresh_status TEXT)`,
		`CREATE TABLE microsoft_enterprise_allocations(resource_id INTEGER,project_id INTEGER,status TEXT)`,
		`CREATE TABLE microsoft_enterprise_project_matches(resource_id INTEGER,project_id INTEGER)`,
		`CREATE TABLE mailmatch_resource_fetch_states(email_resource_id INTEGER,operation_kind TEXT,status TEXT)`,
		`INSERT INTO microsoft_enterprise_resources VALUES(102,'normal',1,'tenant.onmicrosoft.com','idle'),(103,'normal',1,'tenant.onmicrosoft.com','idle')`,
		`INSERT INTO microsoft_enterprise_allocations VALUES(102,10,'released')`,
		`CREATE TABLE project_microsoft_suffix_blacklists(project_id INTEGER)`,
		`CREATE TABLE microsoft_resource_project_matches(project_id INTEGER)`,
		`CREATE TABLE daum_resources(id INTEGER PRIMARY KEY,resource_type TEXT,owner_user_id INTEGER,email_address TEXT,status TEXT,for_sale BOOLEAN,app_password TEXT)`,
		`CREATE TABLE daum_explicit_aliases(id INTEGER PRIMARY KEY,resource_id INTEGER,email_address TEXT,email_domain TEXT,status TEXT)`,
		`CREATE TABLE project_daum_suffix_blacklists(project_id INTEGER,suffix TEXT)`,
		`DROP TABLE daum_allocations`,
		`CREATE TABLE daum_allocations(resource_id INTEGER,project_id INTEGER,email TEXT,guard_type TEXT,status TEXT)`,
		`INSERT INTO daum_resources VALUES(101,'daum',1,'public@daum.net','normal',1,'fixture')`,
		`INSERT INTO daum_explicit_aliases VALUES(1,101,'public@hanmail.net','hanmail.net','normal'),(2,101,'public@hanmail2.net','hanmail2.net','normal')`,
		`INSERT INTO daum_allocations VALUES(101,10,'public@hanmail.net','daum','released')`,
	} {
		require.NoError(t, db.Exec(query).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO allocation_daily_usages VALUES(100,?,'domain','domain_mailbox',3)`, time.Now().UTC().Format("2006-01-02")).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo, ctx := NewRepo(db, client), context.Background()
	seed := func(key string, value any) {
		t.Helper()
		payload, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, client.Set(ctx, key, payload, 7*24*time.Hour).Err())
	}
	seed(fastInventoryBaseKey, fastInventoryBase{ComputedAt: time.Now().UTC(), EligibleResources: 20, AliasesBySuffix: map[string]int64{"outlook.com": 30}})
	seed(fastInventoryHistoryPrefix+"10", fastInventoryHistory{ComputedAt: time.Now().UTC(), MainUsed: 2, AliasBySuffix: map[string]int64{"outlook.com": 3}})
	_, args := iCloudForwardingDomainCondition("ia.forward_to_email")
	config, _ := json.Marshal(args)
	seed(iCloudInventorySnapshotKey, iCloudInventorySnapshot{ComputedAt: time.Now().UTC(), Forwarding: string(config), EligibleResources: 2, Aliases: 25, Used: map[uint]int64{10: 4}, Active: map[uint]int64{10: 1}})
	cache := NewInventoryCache(client)
	legacy := &allocapp.ProjectProductInventoryTotals{ProjectID: 10, Items: []allocapp.ProductInventoryTotal{{ProductID: 21, ProductType: coredomain.ProductTypeICloud}}}
	require.NoError(t, cache.SetProductInventoryTotals(ctx, 10, legacy, 7*24*time.Hour))
	got, err := repo.GetFastProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	want := map[coredomain.ProductType]int64{
		coredomain.ProductTypeMicrosoft: 45, coredomain.ProductTypeMicrosoftEnterprise: 1,
		coredomain.ProductTypeICloud: 21, coredomain.ProductTypeGmail: 1,
		coredomain.ProductTypeGmailVariant: allocapp.GmailVariantInventory + 3, coredomain.ProductTypeDomain: 17,
		coredomain.ProductTypeProto: 3, coredomain.ProductTypeDaum: 2,
	}
	require.Len(t, got.Items, len(want))
	var total int64
	for _, item := range got.Items {
		require.Equal(t, want[item.ProductType], item.TotalAvailable, string(item.ProductType))
		require.Equal(t, item.TotalAvailable, item.PublicAvailable)
		total += item.TotalAvailable
	}
	require.Equal(t, total, got.TotalAvailable)
	require.NotNil(t, got.SourceRefreshedAt)
	require.NoError(t, cache.RefreshProductInventoryTotals(ctx, 10, got, 7*24*time.Hour))
	// One provider's failure must preserve its source time while healthy providers update.
	protoObserved := got.SourceRefreshedAt[coredomain.ProductTypeProto]
	for _, key := range server.Keys() {
		if !strings.HasPrefix(key, inventoryCacheKeyPrefix+"other:10:") {
			continue
		}
		var group otherInventoryGroup
		require.NoError(t, json.Unmarshal([]byte(client.Get(ctx, key).Val()), &group))
		group.AttemptedAt = time.Now().Add(-2 * time.Minute)
		seed(key, group)
	}
	server.FastForward(time.Second)
	require.NoError(t, db.Exec(`DROP TABLE proto_resources`).Error)
	seed(fastInventoryBaseKey, fastInventoryBase{ComputedAt: time.Now().UTC(), EligibleResources: 30, AliasesBySuffix: map[string]int64{"outlook.com": 30}})
	seed(iCloudInventorySnapshotKey, iCloudInventorySnapshot{ComputedAt: time.Now().UTC(), Forwarding: string(config), Aliases: 35, Used: map[uint]int64{10: 4}})
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(cache)
	require.NoError(t, cache.AdvanceInventory(ctx, []allocapp.InventoryCacheEntry{{Kind: allocapp.InventoryCacheProducts, ProjectID: 10}}))
	refreshed, err := useCase.RefreshInventoryCacheBefore(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, refreshed.Failed)
	require.ErrorContains(t, refreshed.LastError, "proto")
	retained, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.True(t, retained.RefreshPending)
	require.Equal(t, got.TotalAvailable+20, retained.TotalAvailable)
	require.Len(t, retained.Items, len(want))
	require.True(t, protoObserved.Equal(retained.SourceRefreshedAt[coredomain.ProductTypeProto]))
	for _, item := range retained.Items {
		if item.ProductType == coredomain.ProductTypeMicrosoft {
			require.EqualValues(t, 55, item.TotalAvailable)
		}
		if item.ProductType == coredomain.ProductTypeICloud {
			require.EqualValues(t, 31, item.TotalAvailable)
		}
	}
	// Cold failure: omit only Proto, without producing a fabricated zero or losing Gmail variants.
	require.NoError(t, client.Del(ctx, inventoryCacheKeyPrefix+"other:10:proto").Err())
	server.FastForward(time.Second)
	partial, err := repo.GetFastProductInventoryTotals(ctx, 10)
	require.ErrorContains(t, err, "proto")
	require.Len(t, partial.Items, len(want)-1)
	for _, item := range partial.Items {
		require.NotEqual(t, coredomain.ProductTypeProto, item.ProductType)
		if item.ProductType == coredomain.ProductTypeGmailVariant {
			require.EqualValues(t, allocapp.GmailVariantInventory+3, item.TotalAvailable)
		}
	}
}

func TestICloudActiveOrdersIncludePrivateAndDisabledResources(t *testing.T) {
	repo, server := iCloudSnapshotTestRepo(t)
	require.NoError(t, repo.db.Exec(`INSERT INTO icloud_allocations VALUES
(4,4,10,'allocated'),(5,5,10,'allocated'),(6,9999,10,'allocated')`).Error)
	snapshot := warmICloudSnapshot(t, repo, server)
	require.EqualValues(t, 2, snapshot.Used[10], "only eligible public history reduces the public pool")
	require.EqualValues(t, 4, snapshot.Active[10], "private, disabled and missing-resource active orders remain active")
	stats, err := repo.GetFastInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.EqualValues(t, 4, stats.ActiveICloudAllocations)
	require.EqualValues(t, 1, stats.ICloud.TotalAvailable)
}

func TestWarmICloudRebuildContinuesThroughWorkerSchedulingAndLongBuilds(t *testing.T) {
	repo, server := iCloudSnapshotTestRepo(t)
	ctx := context.Background()
	previous := warmICloudSnapshot(t, repo, server)
	previous.ComputedAt = time.Now().Add(-2 * time.Hour)
	payload, err := json.Marshal(previous)
	require.NoError(t, err)
	require.NoError(t, repo.redis.Set(ctx, iCloudInventorySnapshotKey, payload, allocapp.InventorySnapshotMaxAge).Err())
	require.NoError(t, repo.db.Exec(`WITH RECURSIVE n(id) AS (SELECT 8 UNION ALL SELECT id+1 FROM n WHERE id<15010)
INSERT INTO icloud_aliases SELECT id,1,'normal','x@forward.test' FROM n`).Error)
	cache := NewInventoryCache(repo.redis)
	uc := allocapp.NewUseCase(repo)
	uc.SetInventoryCache(cache)
	entry := allocapp.InventoryCacheEntry{Kind: allocapp.InventoryCacheProducts, ProjectID: 10}
	require.NoError(t, cache.RequeueInventory(ctx, []allocapp.InventoryCacheEntry{entry}))
	completed := false
	for cycle := 0; cycle < 12; cycle++ {
		server.FastForward(time.Second)
		result, err := uc.RefreshInventoryCacheBefore(ctx, time.Now())
		require.NoError(t, err)
		require.Equal(t, 1, result.Attempted)
		require.Zero(t, result.Failed)
		// Use the raw value so read-repair cannot hide a lost worker continuation.
		totals, err := loadInventoryCache[allocapp.ProjectProductInventoryTotals](ctx, repo.redis, inventoryCacheKey(entry.Kind, entry.ProjectID))
		require.NoError(t, err)
		if result.Updated == 1 {
			require.False(t, totals.RefreshPending)
			require.EqualValues(t, 15004, totals.TotalAvailable)
			completed = true
			break
		}
		require.Equal(t, 1, result.Skipped)
		require.True(t, totals.RefreshPending)
		require.EqualValues(t, 1, totals.TotalAvailable, "keep serving the completed snapshot until all pages finish")
		require.True(t, previous.ComputedAt.Equal(totals.SourceRefreshedAt[coredomain.ProductTypeICloud]))
		score := repo.redis.ZScore(ctx, inventoryCacheScheduleKey, inventoryCacheKey(entry.Kind, entry.ProjectID)).Val()
		require.LessOrEqual(t, score, float64(time.Now().UnixMilli()))
		if cycle == 0 {
			var state iCloudInventoryBuild
			data, err := repo.redis.Get(ctx, iCloudInventorySnapshotKey+":build").Bytes()
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &state))
			require.EqualValues(t, 5000, state.AliasCursor)
			state.ComputedAt = time.Now().Add(-2 * time.Hour)
			state.UpdatedAt = time.Now().Add(-5 * time.Minute)
			data, err = json.Marshal(state)
			require.NoError(t, err)
			require.NoError(t, repo.redis.Set(ctx, iCloudInventorySnapshotKey+":build", data, 24*time.Hour).Err())
			server.FastForward(65 * time.Minute)
		}
		if cycle == 1 {
			var state iCloudInventoryBuild
			data, err := repo.redis.Get(ctx, iCloudInventorySnapshotKey+":build").Bytes()
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &state))
			require.EqualValues(t, 10000, state.AliasCursor, "a healthy build older than one hour must retain progress")
		}
	}
	require.True(t, completed, "a warm rebuild must eventually publish and stop retrying")
}

func TestPartialInventoryDoesNotRenewAnExpiredProvider(t *testing.T) {
	repo, _ := iCloudSnapshotTestRepo(t)
	ctx := context.Background()
	now, old := time.Now().UTC(), time.Now().Add(-8*24*time.Hour)
	cache := NewInventoryCache(repo.redis)
	totals := &allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 105, RefreshPending: true,
		SourceRefreshedAt: map[coredomain.ProductType]time.Time{coredomain.ProductTypeICloud: old, coredomain.ProductTypeGmail: now},
		Items: []allocapp.ProductInventoryTotal{
			{ProductID: 20, ProductType: coredomain.ProductTypeICloud, TotalAvailable: 100},
			{ProductID: 21, ProductType: coredomain.ProductTypeGmail, TotalAvailable: 5},
		}}
	require.NoError(t, cache.RefreshProductInventoryTotals(ctx, 10, totals, allocapp.InventorySnapshotMaxAge))
	got, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, coredomain.ProductTypeGmail, got.Items[0].ProductType)
	require.EqualValues(t, 5, got.TotalAvailable)
}
