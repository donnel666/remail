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
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestICloudDoesNotCountAlreadyUsedNewlyPublicAlias(t *testing.T) {
	r, server := iCloudSnapshotTestRepo(t)
	ctx := context.Background()
	require.NoError(t, r.db.Exec(`WITH RECURSIVE n(id) AS (SELECT 8 UNION ALL SELECT id+1 FROM n WHERE id<5001) INSERT INTO icloud_aliases SELECT id,1,'disabled','x@forward.test' FROM n`).Error)
	require.NoError(t, r.db.Exec(`INSERT INTO icloud_aliases VALUES(5002,2,'normal','x@forward.test')`).Error)
	condition, args := iCloudForwardingDomainCondition("ia.forward_to_email")
	config, _ := json.Marshal(args)
	_, err := r.advanceICloudInventory(ctx, condition, args, string(config))
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	// This alias was private until it was consumed. It was never publicly available to this project.
	require.NoError(t, r.db.Exec(`INSERT INTO icloud_allocations VALUES(4,5002,10,'released')`).Error)
	require.NoError(t, r.db.Exec(`UPDATE icloud_resources SET for_sale=TRUE WHERE id=2`).Error)
	snapshot := warmICloudSnapshot(t, r, server)
	require.EqualValues(t, 1, snapshot.Aliases-snapshot.Used[10], "a newly public but already-used alias cannot add available stock")
}
func TestProductSnapshotSurvivesLossOfStatsCache(t *testing.T) {
	r, _ := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	cache := NewInventoryCache(r.redis)
	observed := time.Now().Add(-time.Hour)
	require.NoError(t, cache.SetProductInventoryTotals(ctx, 10, &allocapp.ProjectProductInventoryTotals{
		ProjectID: 10, TotalAvailable: 47, SourceRefreshedAt: map[core.ProductType]time.Time{core.ProductTypeMicrosoft: observed},
		Items: []allocapp.ProductInventoryTotal{{ProductID: 20, ProductType: core.ProductTypeMicrosoft, TotalAvailable: 47, PublicAvailable: 47}},
	}, 7*24*time.Hour))
	require.Zero(t, r.redis.Exists(ctx, inventoryCacheKey(allocapp.InventoryCacheStats, 10)).Val())
	uc := allocapp.NewUseCase(r)
	uc.SetInventoryCache(cache)
	result, err := uc.RefreshInventoryCacheBefore(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Skipped)
	got, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got.Items, 1, "a valid products snapshot must survive a missing independent stats key")
	require.EqualValues(t, 47, got.TotalAvailable)
}
func TestPageSizeRecoversAfterTransientTimeouts(t *testing.T) {
	r, server := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	require.NoError(t, r.db.Exec(`WITH RECURSIVE n(id) AS (SELECT 11 UNION ALL SELECT id+1 FROM n WHERE id<110) INSERT INTO microsoft_resources SELECT id,'outlook.com','pending',0,'idle' FROM n`).Error)
	failures := 12
	require.NoError(t, r.db.Callback().Query().Before("gorm:query").Register("review:transient-timeout", func(db *gorm.DB) {
		if failures > 0 {
			failures--
			_ = db.AddError(context.DeadlineExceeded)
		}
	}))
	for range 12 {
		server.FastForward(time.Second)
		_, err := r.advanceMicrosoftInventory(ctx)
		require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	}
	for range 32 {
		server.FastForward(time.Second)
		_, err := r.advanceMicrosoftInventory(ctx)
		require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	}
	data, err := r.redis.Get(ctx, microsoftSnapshotKey+":build").Bytes()
	require.NoError(t, err)
	var state microsoftSnapshotBuild
	require.NoError(t, json.Unmarshal(data, &state))
	require.Greater(t, state.PageSize, 1, "recovered workers must not remain at one resource per 250ms for millions of rows")
}

func TestManualRefreshInvalidatesAllRequestedProducts(t *testing.T) {
	db := newProtoSuffixInventoryDB(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`ALTER TABLE projects ADD COLUMN name TEXT DEFAULT 'Proto'`).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	r := NewRepo(db, client)
	cache := NewInventoryCache(client)
	uc := allocapp.NewUseCase(r)
	uc.SetInventoryCache(cache)
	original, err := r.GetFastProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 3, original.TotalAvailable)
	require.NoError(t, cache.SetProductInventoryTotals(ctx, 10, original, 7*24*time.Hour))
	require.NoError(t, client.Set(ctx, inventoryProjectOrderActivitySampledKey, "1", 0).Err())
	require.NoError(t, db.Exec(`UPDATE proto_resources SET for_sale=FALSE`).Error)
	_, err = uc.TriggerInventoryRefresh(ctx, 10)
	require.NoError(t, err)
	server.FastForward(time.Second) // The queued continuation still respects the shared query budget.
	refreshed, err := uc.RefreshInventoryCache(ctx)
	require.NoError(t, err)
	require.Zero(t, refreshed.Failed)
	got, err := cache.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Zero(t, got.TotalAvailable, "explicit refresh after unpublishing must not report the old pool as successfully refreshed")
}

func TestStatsFallbackDoesNotRequireWarmProducts(t *testing.T) {
	r, _ := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	cache := NewInventoryCache(r.redis)
	observed := time.Now().Add(-time.Hour)
	require.NoError(t, r.db.Exec(`ALTER TABLE projects ADD COLUMN name TEXT DEFAULT 'Project'`).Error)
	require.NoError(t, cache.SetInventoryStats(ctx, 10, &allocapp.InventoryStats{ProjectID: 10, Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true, MainAvailable: 40, ExplicitAliasAvailable: 7, TotalAvailable: 47}, TotalAvailable: 47, SourceRefreshedAt: map[core.ProductType]time.Time{core.ProductTypeMicrosoft: observed}}, 7*24*time.Hour))
	uc := allocapp.NewUseCase(r)
	uc.SetInventoryCache(cache)
	_, err := uc.RefreshInventoryCache(ctx)
	require.NoError(t, err)
	got, err := cache.GetInventoryStats(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 47, got.Microsoft.TotalAvailable, "schedule initialization must not let a cold products placeholder erase valid Microsoft stats")
}

func TestMicrosoftFallbackKeepsEachViewsOwnObservationTime(t *testing.T) {
	r, _ := microsoftSnapshotTestRepo(t)
	ctx := context.Background()
	cache := NewInventoryCache(r.redis)
	statsAt, productsAt := time.Now().Add(-3*time.Hour), time.Now().Add(-time.Hour)
	require.NoError(t, cache.SetInventoryStats(ctx, 10, &allocapp.InventoryStats{ProjectID: 10, Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true, TotalAvailable: 50}, TotalAvailable: 50, SourceRefreshedAt: map[core.ProductType]time.Time{core.ProductTypeMicrosoft: statsAt}}, 7*24*time.Hour))
	require.NoError(t, cache.SetProductInventoryTotals(ctx, 10, &allocapp.ProjectProductInventoryTotals{ProjectID: 10, TotalAvailable: 47, SourceRefreshedAt: map[core.ProductType]time.Time{core.ProductTypeMicrosoft: productsAt}, Items: []allocapp.ProductInventoryTotal{{ProductID: 20, ProductType: core.ProductTypeMicrosoft, TotalAvailable: 47, PublicAvailable: 47}}}, 7*24*time.Hour))
	stats, products, err := r.fastProjectInventory(ctx, 10)
	require.ErrorIs(t, err, platform.ErrBackgroundExecutionDeferred)
	require.True(t, statsAt.Equal(stats.SourceRefreshedAt[core.ProductTypeMicrosoft]))
	require.True(t, productsAt.Equal(products.SourceRefreshedAt[core.ProductTypeMicrosoft]))
	require.EqualValues(t, 50, stats.TotalAvailable)
	require.EqualValues(t, 47, products.TotalAvailable)
}
