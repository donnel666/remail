package infra

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func seedICloudAllocationInventory(t *testing.T, db *gorm.DB, resources, aliases int) {
	t.Helper()
	seedAllocBase(t, db, "icloud", 1, 0, 0)
	seedDomainResourcesWithPurpose(t, db, 1, 2000, 5, "binding")
	previous := runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, "")
	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, "d2000.example.com")
	t.Cleanup(func() { runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, previous) })
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id)
WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < 255)
SELECT 10000 + hi.n * 256 + lo.n, 'icloud', 1 FROM seq hi CROSS JOIN seq lo
WHERE hi.n * 256 + lo.n < ?`, resources).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resources(id, primary_email, expire_at, for_sale, status, alias_count)
SELECT id, CONCAT('account-', id, '@icloud.com'), UTC_TIMESTAMP() - INTERVAL 1 DAY, TRUE, 'normal', ?
FROM email_resources WHERE type = 'icloud'`, aliases).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_aliases(resource_id, anonymous_id, email, forward_to_email, status)
WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n + 1 < ?)
SELECT ir.id, seq.n, CONCAT('alias-', ir.id, '-', seq.n, '@icloud.com'), 'inbox@d2000.example.com', 'normal'
FROM icloud_resources ir CROSS JOIN seq`, aliases).Error)
}

// REMAIL_ICLOUD_BENCHMARK=1 runs 300 workers against 32,768 accounts and 262,144
// aliases, including 196,608 historical allocations, with the same checks.
func TestICloudAllocationConcurrentMySQL(t *testing.T) {
	resources, aliases, requests, workers := 512, 64, 200, 100
	if os.Getenv("REMAIL_ICLOUD_BENCHMARK") == "1" {
		resources, aliases, requests, workers = 32768, 8, 1500, 300
	}
	db := newAllocMySQLTestDB(t)
	seedICloudAllocationInventory(t, db, resources, aliases)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type)
SELECT CONCAT('icloud-history-', id), 'icloud'
FROM icloud_aliases WHERE CAST(anonymous_id AS UNSIGNED) < ?`, aliases*3/4).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_allocations(order_no, project_id, product_id, resource_id, alias_id, supply_scope, email, status, released_at)
SELECT CONCAT('icloud-history-', id), 10, 20, resource_id, id, 'public', email, 'released', UTC_TIMESTAMP()
FROM icloud_aliases WHERE CAST(anonymous_id AS UNSIGNED) < ?`, aliases*3/4).Error)

	// Exercise the online migration against existing accounts and aliases too.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, allocMigrationsDir(t), 142))
	require.NoError(t, goose.UpTo(sqlDB, allocMigrationsDir(t), 144))

	capture := &candidateSQLCapture{Interface: db.Logger, match: "FROM icloud_"}
	repo := NewRepo(db.Session(&gorm.Session{Logger: capture}))
	var bucket uint16
	require.NoError(t, db.Table("icloud_resources").Select("alloc_bucket").Order("id").Limit(1).Scan(&bucket).Error)
	candidates, err := repo.ListICloudSourceCandidates(context.Background(), 10, 2, domain.SupplyScopePublic,
		[]uint16{bucket}, 4)
	require.NoError(t, err)
	require.NotEmpty(t, candidates)
	require.NoError(t, repo.WithTx(context.Background(), func(ctx context.Context) error {
		locked, err := repo.TryLockResourceRoot(ctx, candidates[0], domain.AllocationTypeICloud)
		if err != nil {
			return err
		}
		require.True(t, locked)
		alias, err := repo.LockICloudCandidate(ctx, candidates[0], 10, 2, domain.SupplyScopePublic)
		require.NotNil(t, alias)
		return err
	}))
	require.Len(t, capture.queries, 2)
	requireExplainTargetUsesIndex(t, db, capture.queries[0], "ir", "idx_icloud_alloc_bucket")
	requireExplainTargetUsesIndex(t, db, capture.queries[1], "ia", "idx_icloud_aliases_inventory")
	require.LessOrEqual(t, explainAnalyzeTargetWork(t, db, capture.queries[0], "ir"), float64(4))
	require.LessOrEqual(t, explainAnalyzeTargetWork(t, db, capture.queries[0], "ia"), float64(len(candidates)*aliases))
	require.LessOrEqual(t, explainAnalyzeTargetWork(t, db, capture.queries[1], "ia"), float64(aliases))
	expanded := make([]uint16, allocapp.ICloudExpansionBuckets)
	for i := range expanded {
		expanded[i] = (bucket + uint16(i+1)) % allocapp.ICloudBucketCount
	}
	var selectedRoots int64
	require.NoError(t, db.Table("icloud_resources").Where("alloc_bucket IN ?", expanded).Count(&selectedRoots).Error)
	require.Less(t, selectedRoots, int64(resources))
	for _, scope := range []domain.SupplyScope{domain.SupplyScopePublic, domain.SupplyScopeOwned} {
		capture.queries = nil
		_, err := repo.ListICloudSourceCandidates(context.Background(), 10, 1, scope, expanded, 8)
		require.NoError(t, err)
		require.Len(t, capture.queries, 1)
		requireExplainTargetUsesIndex(t, db, capture.queries[0], "ir", "idx_icloud_alloc_bucket")
		rootWorkLimit := selectedRoots
		if os.Getenv("REMAIL_ICLOUD_BENCHMARK") != "1" {
			// Tiny tables may use a cheaper covering-index scan. At scale require
			// a range scan; alias work must stay in the selected buckets at any size.
			rootWorkLimit = int64(resources)
		}
		require.LessOrEqual(t, explainAnalyzeTargetWork(t, db, capture.queries[0], "ir"), float64(rootWorkLimit))
		require.LessOrEqual(t, explainAnalyzeTargetWork(t, db, capture.queries[0], "ia"), float64(selectedRoots)*float64(aliases))
	}

	sqlDB.SetMaxOpenConns(workers + 16)
	sqlDB.SetMaxIdleConns(workers)
	useCase := allocapp.NewUseCase(NewRepo(db.Session(&gorm.Session{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})))
	deadlocks := innodbMetricCount(t, db, "lock_deadlocks")
	timeouts := innodbMetricCount(t, db, "lock_timeouts")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	type outcome struct {
		allocation *domain.UnifiedAllocation
		err        error
		duration   time.Duration
	}
	jobs := make(chan int, requests)
	results := make(chan outcome, requests)
	for i := range requests {
		jobs <- i
	}
	close(jobs)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range jobs {
				began := time.Now()
				allocation, err := useCase.Allocate(ctx, allocapp.AllocateCommand{
					OrderNo: fmt.Sprintf("icloud-concurrent-%d", i), BuyerUserID: 2,
					ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
				})
				results <- outcome{allocation, err, time.Since(began)}
			}
		}()
	}
	began := time.Now()
	close(start)
	wg.Wait()
	elapsed := time.Since(began)
	close(results)
	seen := make(map[string]bool, requests)
	latencies := make([]time.Duration, 0, requests)
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.allocation)
		require.False(t, seen[result.allocation.Email], "duplicate allocation: %s", result.allocation.Email)
		seen[result.allocation.Email] = true
		latencies = append(latencies, result.duration)
	}
	require.Len(t, seen, requests)
	var count int64
	require.NoError(t, db.Table("icloud_allocations").Where("status = 'allocated'").Count(&count).Error)
	require.EqualValues(t, requests, count)
	require.Equal(t, deadlocks, innodbMetricCount(t, db, "lock_deadlocks"))
	require.Equal(t, timeouts, innodbMetricCount(t, db, "lock_timeouts"))
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("accounts=%d aliases=%d history=%d concurrency=%d allocations=%d elapsed=%s throughput=%.1f/s p50=%s p95=%s p99=%s",
		resources, resources*aliases, resources*aliases*3/4, workers, requests, elapsed,
		float64(requests)/elapsed.Seconds(), latencies[requests/2], latencies[requests*95/100], latencies[requests*99/100])
}

func TestICloudAllocationBoundedFallbackAndBusyRootsMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedICloudAllocationInventory(t, db, 4, 2)
	repo := NewRepo(db)
	// CRC32 buckets for IDs 10000..10003 are 69, 211, 105, 255. Starting
	// at 255 checks 255/0/1/2, then only 3..102 in the expanded query.
	held := db.Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, held.Error)
	t.Cleanup(func() { _ = held.Rollback().Error })
	var id uint
	require.NoError(t, held.Raw("SELECT id FROM email_resources WHERE id = 10003 FOR UPDATE").Scan(&id).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	useCase := allocapp.NewUseCase(repo)
	cmd := allocapp.AllocateCommand{
		OrderNo:     testmysql.AllocationOrderNo("icloud-wrap", 10, "icloud", 255, allocapp.ICloudBucketCount),
		BuyerUserID: 2, ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
	}
	allocation, err := useCase.Allocate(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, uint(10000), allocation.ResourceID)
	require.NoError(t, held.Rollback().Error)
	repeated, err := useCase.Allocate(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, allocation.ID, repeated.ID)

	// A private account in the selected window remains ahead of public supply.
	require.NoError(t, db.Exec("UPDATE email_resources SET owner_user_id = 2 WHERE id = ?", allocation.ResourceID).Error)
	require.NoError(t, db.Exec("UPDATE icloud_resources SET for_sale = FALSE WHERE id = ?", allocation.ResourceID).Error)
	cmd.OrderNo = testmysql.AllocationOrderNo("icloud-private", 10, "icloud", 69, allocapp.ICloudBucketCount)
	cmd.SupplyScopes = []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic}
	private, err := useCase.Allocate(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, allocation.ResourceID, private.ResourceID)
	require.Equal(t, domain.SupplyScopeOwned, private.SupplyScope)
	require.NotEqual(t, allocation.Email, private.Email)

	// An exhausted old account must not hide a later eligible account behind
	// LIMIT 1 in the same bucket (IDs 10000 and 10572 both hash to bucket 69).
	require.NoError(t, db.Exec("UPDATE icloud_resources SET for_sale = TRUE, last_allocated_at = '2000-01-01' WHERE id = 10000").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id, type, owner_user_id) VALUES (10572, 'icloud', 1)").Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resources(id, primary_email, expire_at, for_sale, status, last_allocated_at)
VALUES (10572, 'available@icloud.com', UTC_TIMESTAMP(), TRUE, 'normal', UTC_TIMESTAMP())`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_aliases(resource_id, anonymous_id, email, forward_to_email, status)
VALUES (10572, 'available', 'available-alias@icloud.com', 'inbox@d2000.example.com', 'normal')`).Error)
	eligible, err := repo.ListICloudSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, []uint16{69}, 1)
	require.NoError(t, err)
	require.Equal(t, []uint{10572}, eligible)

	// All public stock is outside 106..209: do not extend the bounded miss.
	cmd.OrderNo = testmysql.AllocationOrderNo("icloud-outside", 10, "icloud", 106, allocapp.ICloudBucketCount)
	cmd.SupplyScopes = nil
	_, err = useCase.Allocate(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	require.NotErrorIs(t, err, domain.ErrDefinitiveInventoryExhausted)

	// Removing the live forwarding authorization must stop new allocations.
	require.NoError(t, db.Exec("UPDATE domain_resources SET status = 'disabled' WHERE id = 2000").Error)
	cmd.OrderNo = testmysql.AllocationOrderNo("icloud-disabled", 10, "icloud", 105, allocapp.ICloudBucketCount)
	_, err = useCase.Allocate(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
}

func TestICloudCandidatesRequireBoundedBuckets(t *testing.T) {
	for _, buckets := range [][]uint16{nil, {}, make([]uint16, 101), {allocapp.ICloudBucketCount}} {
		_, err := NewRepo(nil).ListICloudSourceCandidates(context.Background(), 10, 2, domain.SupplyScopePublic, buckets, 4)
		require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
	}
}
