package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var (
	allocMySQLTestServer       = testmysql.New("remail_alloc_test")
	allocLegacyMySQLTestServer = testmysql.New("remail_alloc_legacy_test")
)

func TestMain(m *testing.M) {
	code := m.Run()
	_ = allocMySQLTestServer.Close(context.Background())
	_ = allocLegacyMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newAllocMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return allocMySQLTestServer.Database(t, allocMigrationsDir(t))
}

func newAllocLegacyMigrationTestDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	migrationsDir := testmysql.MigrationsThrough(t, allocMigrationsDir(t), 65)
	return allocLegacyMySQLTestServer.Database(t, migrationsDir), migrationsDir
}

func innodbMetricCount(t *testing.T, db *gorm.DB, name string) uint64 {
	t.Helper()
	var count uint64
	require.NoError(t, db.Raw(`
SELECT COUNT
FROM information_schema.innodb_metrics
WHERE NAME = ?`, name).Scan(&count).Error)
	return count
}

func allocMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "migrations"))
}

func TestMicrosoftMainAllocationConcurrentMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 10_000, true, "normal")
	deadlocksBefore := innodbMetricCount(t, db, "lock_deadlocks")
	timeoutsBefore := innodbMetricCount(t, db, "lock_timeouts")

	uc := allocapp.NewUseCase(NewRepo(db))
	const workers = 100
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
				OrderNo:          fmt.Sprintf("ord-ms-%03d", i),
				BuyerUserID:      2,
				ProjectProductID: 20,
				SupplyScope:      domain.SupplyScopePublic,
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result.Email
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	seen := map[string]struct{}{}
	for email := range results {
		require.NotContains(t, seen, email)
		seen[email] = struct{}{}
	}
	require.Len(t, seen, workers)

	var active int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM microsoft_allocations WHERE status = 'allocated'").Scan(&active).Error)
	require.Equal(t, int64(workers), active)
	require.Equal(t, deadlocksBefore, innodbMetricCount(t, db, "lock_deadlocks"), "allocation must not rely on deadlock retries")
	require.Equal(t, timeoutsBefore, innodbMetricCount(t, db, "lock_timeouts"), "allocation must not rely on lock-timeout retries")
}

func TestMicrosoftMainUsesAliasWhenMainIsActiveInAnotherProjectMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (1000, 1, 'alias1000@example.com', 'normal')`).Error)

	uc := allocapp.NewUseCase(NewRepo(db))
	mainAllocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-main-project-10", BuyerUserID: 2, ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "main", mainAllocation.Mailbox)

	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type)
VALUES (11, 'Other Project', 'alloc', 'listed', 'public')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (21, 11, 'microsoft', 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 1, 0, 0)`).Error)

	aliasAllocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-alias-project-11", BuyerUserID: 2, ProjectProductID: 21, SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "alias", aliasAllocation.Mailbox)
	require.Equal(t, "alias1000@example.com", aliasAllocation.Email)
}

func TestMicrosoftAllocationAndInventoryRespectBlockingMaintenanceMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 5, true, "normal")
	repo := NewRepo(db)
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 1000).Update("status", "validating").Error)
	require.NoError(t, db.Table("microsoft_resources").Where("id = ?", 1001).Update("token_refresh_status", "pending").Error)
	require.NoError(t, db.Exec(`
INSERT INTO mailmatch_resource_fetch_states(email_resource_id, status, generation, operation_kind)
VALUES
    (1002, 'processing', 1, 'resource_history'),
    (1004, 'processing', 1, 'resource_fetch')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_alias_schedules(resource_id, status, next_run_at)
VALUES (1003, 'running', UTC_TIMESTAMP(3))`).Error)

	stats, err := repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.Microsoft.EligibleResources)
	require.Equal(t, int64(2), stats.Microsoft.MainAvailable)
	require.Equal(t, int64(2), stats.TotalAvailable)
	totals, err := repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), totals.TotalAvailable)
	require.Equal(t, []allocapp.ProductInventorySuffixTotal{{Suffix: "example.com", TotalAvailable: 2, PublicAvailable: 2}}, totals.Items[0].Suffixes)
	uc := allocapp.NewUseCase(repo)
	aliasMaintenanceAllocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-alias-maintenance", BuyerUserID: 2, ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, uint(1003), aliasMaintenanceAllocation.ResourceID)
	manualFetchAllocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-manual-fetch", BuyerUserID: 2, ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, uint(1004), manualFetchAllocation.ResourceID)
}

func TestGmailUnifiedAllocationInventoryMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "gmail", 1, 1, 1)
	seedGmailResources(t, db, []gmailResourceSeed{
		{ID: 1000, OwnerUserID: 1, Email: "firstname@gmail.com", ForSale: true},
		{ID: 1001, OwnerUserID: 1, Email: "ab@gmail.com", ForSale: true},
		{ID: 1002, OwnerUserID: 3, Email: "ignored@gmail.com", ForSale: true},
	})

	repo := NewRepo(db)
	uc := allocapp.NewUseCase(repo)
	createdAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	releasedAt := createdAt.Add(time.Hour)
	for _, history := range []allocapp.HistoricalGmailAllocationCommand{
		{ProjectID: 10, ProductID: 20, ResourceID: 1000, Mailbox: domain.GmailMailboxMain, Email: "firstname@gmail.com", CreatedAt: createdAt, ReleasedAt: releasedAt},
		{ProjectID: 10, ProductID: 20, ResourceID: 1000, Mailbox: domain.GmailMailboxDot, Email: "first.name@gmail.com", CreatedAt: createdAt, ReleasedAt: releasedAt},
	} {
		allocation, err := uc.ImportHistoricalGmailAllocation(context.Background(), history)
		require.NoError(t, err)
		require.NotNil(t, allocation)
		require.Equal(t, domain.AllocationTypeGmail, allocation.Type)
		require.Equal(t, domain.AllocationStatusReleased, allocation.Status)
	}

	assertInventory := func(codeEnabled, purchaseEnabled bool) {
		t.Helper()
		stats, err := repo.GetInventoryStats(context.Background(), 10)
		require.NoError(t, err)
		require.True(t, stats.Gmail.Enabled)
		require.Equal(t, codeEnabled, stats.Gmail.CodeEnabled)
		require.Equal(t, purchaseEnabled, stats.Gmail.PurchaseEnabled)
		require.Equal(t, int64(2), stats.Gmail.EligibleResources)
		require.Equal(t, int64(1), stats.Gmail.MainAvailable)
		require.Equal(t, int64(8), stats.Gmail.DotAvailable)
		require.Equal(t, int64(2), stats.Gmail.PlusAvailable)
		require.Equal(t, int64(11), stats.Gmail.TotalAvailable)
		require.Equal(t, int64(11), stats.TotalAvailable)

		totals, err := repo.GetProductInventoryTotals(context.Background(), 10)
		require.NoError(t, err)
		require.Len(t, totals.Items, 1)
		item := totals.Items[0]
		require.NotNil(t, item.CodeAvailable)
		require.NotNil(t, item.PurchaseAvailable)
		if codeEnabled {
			require.Equal(t, int64(11), *item.CodeAvailable)
		} else {
			require.Zero(t, *item.CodeAvailable)
		}
		if purchaseEnabled {
			require.Equal(t, int64(11), *item.PurchaseAvailable)
		} else {
			require.Zero(t, *item.PurchaseAvailable)
		}
	}

	assertInventory(true, false)
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).
		Updates(map[string]any{"main_weight": 0, "dot_weight": 0, "plus_weight": 1}).Error)
	active, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-gmail-active", BuyerUserID: 2, ProjectProductID: 20,
		ServiceMode: domain.GmailServiceModeCode, SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "plus", active.Mailbox)
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).
		Updates(map[string]any{"main_weight": 1, "dot_weight": 1, "plus_weight": 1}).Error)
	stats, err := repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.ActiveGmailAllocations)
	_, err = uc.ReleaseByOrder(context.Background(), active.OrderNo)
	require.NoError(t, err)
	stats, err = repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, stats.ActiveGmailAllocations)

	require.NoError(t, db.Table("project_products").Where("id = ?", 20).
		Updates(map[string]any{"code_enabled": false, "purchase_enabled": true}).Error)
	assertInventory(false, true)
}

func TestAllocationAllowsDelistedProductOnlyForExistingOrderMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	require.NoError(t, db.Table("project_products").Where("id = ?", 20).Update("status", "disabled").Error)

	uc := allocapp.NewUseCase(NewRepo(db))
	_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-delisted-product-new",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrProjectNotAllocatable)

	allocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:              "ord-delisted-product-existing",
		BuyerUserID:          2,
		ProjectProductID:     20,
		SupplyScope:          domain.SupplyScopePublic,
		FulfillExistingOrder: true,
	})
	require.NoError(t, err)
	require.Equal(t, uint(1000), allocation.ResourceID)
}

func TestAllocationWaitsForMicrosoftResourceRootLockedByAdminMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")

	adminTx := db.Begin()
	require.NoError(t, adminTx.Error)
	t.Cleanup(func() { _ = adminTx.Rollback().Error })
	var rootID uint
	require.NoError(t, adminTx.Raw(`
SELECT id
FROM email_resources
WHERE id = 1000
FOR UPDATE`).Scan(&rootID).Error)
	require.Equal(t, uint(1000), rootID)

	uc := allocapp.NewUseCase(NewRepo(db))
	allocationDone := make(chan error, 1)
	go func() {
		_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
			OrderNo:          "ord-admin-root-first",
			BuyerUserID:      2,
			ProjectProductID: 20,
			SupplyScope:      domain.SupplyScopePublic,
		})
		allocationDone <- err
	}()

	select {
	case err := <-allocationDone:
		t.Fatalf("allocation finished before the administrator transaction: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	// This is the write window that used to permit a check-then-act race: an
	// allocator could lock the subtype and create an allocation after Core had
	// locked the root but before Core changed the subtype state.
	require.NoError(t, adminTx.Exec("UPDATE microsoft_resources SET status = 'disabled' WHERE id = 1000").Error)
	require.NoError(t, adminTx.Commit().Error)

	select {
	case err := <-allocationDone:
		require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	case <-time.After(5 * time.Second):
		t.Fatal("allocation did not resume after the administrator transaction committed")
	}

	var active int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM microsoft_allocations
WHERE resource_id = 1000 AND status = 'allocated'`).Scan(&active).Error)
	require.Zero(t, active)
}

func TestAdminGuardWaitsForAllocationRootThenSeesActiveAllocationMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")

	repo := NewRepo(db)
	allocationTx := db.Begin()
	require.NoError(t, allocationTx.Error)
	t.Cleanup(func() { _ = allocationTx.Rollback().Error })
	allocationCtx := platform.WithGormTx(context.Background(), allocationTx)
	require.NoError(t, repo.CreateOrderGuard(allocationCtx, "ord-allocation-root-first", domain.AllocationTypeMicrosoft))
	lockedRoot, err := repo.LockResourceRoot(allocationCtx, 1000, domain.AllocationTypeMicrosoft)
	require.NoError(t, err)
	require.True(t, lockedRoot)
	lockedCandidate, err := repo.LockMicrosoftCandidate(allocationCtx, 1000, 10, 2, domain.SupplyScopePublic, domain.MicrosoftMailboxMain, "")
	require.NoError(t, err)
	require.NotNil(t, lockedCandidate)

	guardUseCase := allocapp.NewUseCase(repo)
	adminEntered := make(chan struct{}, 1)
	adminDone := make(chan error, 1)
	adminCtx, cancelAdmin := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAdmin()
	go func() {
		adminDone <- db.WithContext(adminCtx).Transaction(func(tx *gorm.DB) error {
			txCtx := platform.WithGormTx(adminCtx, tx)
			adminEntered <- struct{}{}
			var id uint
			if err := tx.Raw(`
SELECT id
FROM email_resources
WHERE id = 1000
FOR UPDATE`).Scan(&id).Error; err != nil {
				return err
			}
			if id != 1000 {
				return fmt.Errorf("unexpected locked resource root %d", id)
			}
			return guardUseCase.AssertNoActiveAllocations(txCtx, []uint{1000})
		})
	}()
	<-adminEntered
	select {
	case err := <-adminDone:
		t.Fatalf("administrator command passed the allocation-held root early: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	allocation := &domain.MicrosoftAllocation{
		OrderNo:     "ord-allocation-root-first",
		ProjectID:   10,
		ProductID:   20,
		ResourceID:  1000,
		SupplyScope: domain.SupplyScopePublic,
		Mailbox:     domain.MicrosoftMailboxMain,
		Email:       "ms1000@example.com",
		Status:      domain.AllocationStatusAllocated,
	}
	require.NoError(t, repo.CreateMicrosoftAllocation(allocationCtx, allocation))
	require.NoError(t, allocationTx.Commit().Error)

	select {
	case err := <-adminDone:
		require.ErrorIs(t, err, domain.ErrActiveAllocation)
	case <-time.After(5 * time.Second):
		t.Fatal("administrator guard did not resume after allocation committed")
	}
}

func TestResourceAllocationGuardRequiresRootTransactionAndIgnoresReleasedMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")

	repo := NewRepo(db)
	uc := allocapp.NewUseCase(repo)
	require.ErrorIs(t, uc.AssertNoActiveAllocations(context.Background(), []uint{1000}), domain.ErrAllocationTxRequired)

	assertGuard := func(want error) {
		t.Helper()
		err := repo.WithTx(context.Background(), func(txCtx context.Context) error {
			locked, err := repo.LockResourceRoot(txCtx, 1000, domain.AllocationTypeMicrosoft)
			if err != nil {
				return err
			}
			if !locked {
				return errors.New("resource root was not locked")
			}
			return uc.AssertNoActiveAllocations(txCtx, []uint{1000, 0, 1000})
		})
		if want == nil {
			require.NoError(t, err)
			return
		}
		require.ErrorIs(t, err, want)
	}

	assertGuard(nil)
	_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-active-guard",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	assertGuard(domain.ErrActiveAllocation)
	_, err = uc.ReleaseByOrder(context.Background(), "ord-active-guard")
	require.NoError(t, err)
	assertGuard(nil)
}

func TestMicrosoftAllocationSkipsHistoricalProjectMatchMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 2, true, "normal")
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resource_project_matches(
    resource_id, project_id, first_matched_at, last_matched_at,
    evidence_count, last_scanned_at
) VALUES (1000, 10, ?, ?, 1, ?)`, now, now, now).Error)

	result, err := allocapp.NewUseCase(NewRepo(db)).Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-history-exclusion",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, uint(1001), result.ResourceID)
}

func TestMicrosoftAllocationSkipsOnlyHistoricalMailboxEntityMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status) VALUES
    (1000, 1, 'used-alias@example.com', 'normal'),
    (1000, 1, 'free-alias@example.com', 'normal')`).Error)
	var usedAliasID uint
	require.NoError(t, db.Table("explicit_aliases").Select("id").Where("email = ?", "used-alias@example.com").Scan(&usedAliasID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_order_guards(order_no, type) VALUES
    ('history-main', 'microsoft'),
    ('history-alias', 'microsoft')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_allocations(
    order_no, project_id, product_id, resource_id, supply_scope, mailbox,
    explicit_alias_id, email, status, released_at
) VALUES
    ('history-main', 10, 20, 1000, 'public', 'main', NULL, 'ms1000@example.com', 'released', UTC_TIMESTAMP()),
    ('history-alias', 10, 20, 1000, 'public', 'alias', ?, 'used-alias@example.com', 'released', UTC_TIMESTAMP())`, usedAliasID).Error)

	result, err := allocapp.NewUseCase(NewRepo(db)).Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-mailbox-history-exclusion",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "free-alias@example.com", result.Email)
	require.Equal(t, "alias", result.Mailbox)
}

func TestDomainAllocationConcurrentMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResources(t, db, 1, 2000, 120)
	deadlocksBefore := innodbMetricCount(t, db, "lock_deadlocks")
	timeoutsBefore := innodbMetricCount(t, db, "lock_timeouts")

	uc := allocapp.NewUseCase(NewRepo(db))
	const workers = 80
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
				OrderNo:          fmt.Sprintf("ord-domain-%03d", i),
				BuyerUserID:      2,
				ProjectProductID: 20,
				SupplyScope:      domain.SupplyScopePublic,
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result.Email
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	seen := map[string]struct{}{}
	for email := range results {
		require.NotContains(t, seen, email)
		seen[email] = struct{}{}
	}
	require.Len(t, seen, workers)
	require.Equal(t, deadlocksBefore, innodbMetricCount(t, db, "lock_deadlocks"), "allocation must not rely on deadlock retries")
	require.Equal(t, timeoutsBefore, innodbMetricCount(t, db, "lock_timeouts"), "allocation must not rely on lock-timeout retries")
}

func TestSameOrderConcurrentIsIdempotentMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 20, true, "normal")

	uc := allocapp.NewUseCase(NewRepo(db))
	const workers = 40
	results := make(chan *domain.UnifiedAllocation, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
				OrderNo:          "ord-idempotent",
				BuyerUserID:      2,
				ProjectProductID: 20,
				SupplyScope:      domain.SupplyScopePublic,
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	var first *domain.UnifiedAllocation
	for result := range results {
		require.NotNil(t, result)
		if first == nil {
			first = result
			continue
		}
		require.Equal(t, first.Type, result.Type)
		require.Equal(t, first.ID, result.ID)
		require.Equal(t, first.Email, result.Email)
	}
	require.NotNil(t, first)

	var guardCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM allocation_order_guards WHERE order_no = 'ord-idempotent'").Scan(&guardCount).Error)
	require.Equal(t, int64(1), guardCount)
	var allocationCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM microsoft_allocations WHERE order_no = 'ord-idempotent'").Scan(&allocationCount).Error)
	require.Equal(t, int64(1), allocationCount)
}

func TestListActiveByRecipientMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")

	repo := NewRepo(db)
	uc := allocapp.NewUseCase(repo)
	result, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-recipient",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.NoError(t, err)

	items, err := repo.ListActiveByRecipient(context.Background(), result.Email)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, result.OrderNo, items[0].OrderNo)

	_, err = uc.ReleaseByOrder(context.Background(), result.OrderNo)
	require.NoError(t, err)
	items, err = repo.ListActiveByRecipient(context.Background(), result.Email)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestReleaseKeepsMicrosoftMainOutOfItsHistoricalProjectMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")

	uc := allocapp.NewUseCase(NewRepo(db))
	first, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-release-1",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	_, err = uc.ReleaseByOrder(context.Background(), "ord-release-1")
	require.NoError(t, err)
	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-release-same-project",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type)
VALUES (11, 'Other Project', 'alloc', 'listed', 'public')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (21, 11, 'microsoft', 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 1, 0, 0)`).Error)
	second, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-release-other-project",
		BuyerUserID:      2,
		ProjectProductID: 21,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, first.ResourceID, second.ResourceID)
	require.Equal(t, first.Email, second.Email)
}

func TestPublicAllocationExcludesRegularUserResourceMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 3, 1000, 1, true, "normal")

	uc := allocapp.NewUseCase(NewRepo(db))
	_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-public-user-resource",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	require.NoError(t, db.Table("users").Where("id = ?", 3).Updates(map[string]any{
		"role":   "supplier",
		"status": "deleted",
	}).Error)
	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-public-deleted-user-resource",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
}

func TestOwnedAllocationUsesOnlyBuyerPrivateResourceMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 2, 1000, 1, false, "normal")
	seedMicrosoftResources(t, db, 3, 2000, 1, false, "normal")

	uc := allocapp.NewUseCase(NewRepo(db))
	result, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-owned",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopeOwned,
	})
	require.NoError(t, err)
	require.Equal(t, "ms1000@example.com", result.Email)
}

func TestOwnedDomainAllocationUsesOnlyBuyerPrivateResourceMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResourcesWithPurpose(t, db, 2, 2000, 1, "not_sale")
	seedDomainResourcesWithPurpose(t, db, 3, 3000, 1, "not_sale")

	uc := allocapp.NewUseCase(NewRepo(db))
	_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-domain-public-private",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	result, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-domain-owned",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopeOwned,
	})
	require.NoError(t, err)
	require.Equal(t, domain.AllocationTypeDomain, result.Type)
	require.Equal(t, uint(2000), result.ResourceID)
	require.Contains(t, result.Email, "@d2000.example.com")
}

func TestFindOrCreateGeneratedMailboxDoesNotReuseDisabledMailboxMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResourcesWithPurpose(t, db, 2, 2000, 1, "not_sale")
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status)
VALUES (?, ?, ?, ?)`, 2000, 2, "disabled@d2000.example.com", "disabled").Error)

	mailbox, err := NewRepo(db).FindOrCreateGeneratedMailbox(context.Background(), 2000, 2, "disabled@d2000.example.com")
	require.NoError(t, err)
	require.Nil(t, mailbox)
}

func TestGeneratedMailboxCandidatesUseExpandedBucketMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResources(t, db, 1, 4095, 1)
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(id, resource_id, owner_user_id, email, status, alloc_bucket)
VALUES (2047, 4095, 1, 'existing@d4095.example.com', 'normal', 2047)`).Error)

	bucket := uint16(2047)
	candidates, err := NewRepo(db).ListGeneratedMailboxCandidates(context.Background(), 10, 2, domain.SupplyScopePublic, &bucket, 4, "")
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, uint(2047), candidates[0].ID)
	require.Equal(t, uint(4095), candidates[0].ResourceID)
}

func TestFindOrCreateGeneratedMailboxAssignsExpandedBucketMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResources(t, db, 1, 4095, 1)

	mailbox, err := NewRepo(db).FindOrCreateGeneratedMailbox(context.Background(), 4095, 1, "new@d4095.example.com")
	require.NoError(t, err)
	require.NotNil(t, mailbox)
	var bucket uint16
	require.NoError(t, db.Raw("SELECT alloc_bucket FROM generated_mailboxes WHERE id = ?", mailbox.ID).Scan(&bucket).Error)
	require.Equal(t, coredomain.GeneratedMailboxBucket(mailbox.Email), bucket)
}

func TestGeneratedMailboxReuseSkipsProjectEmailHistoryMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResources(t, db, 1, 4095, 1)
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(id, resource_id, owner_user_id, email, status, alloc_bucket)
VALUES
    (2047, 4095, 1, 'active@d4095.example.com', 'normal', 2047),
    (2048, 4095, 1, 'reusable@d4095.example.com', 'normal', 0)`).Error)
	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('ord-active-generated', 'domain')").Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox_id, email)
VALUES ('ord-active-generated', 10, 20, 4095, 'public', 2047, 'active@d4095.example.com')`).Error)

	repo := NewRepo(db)
	bucket := uint16(2047)
	candidates, err := repo.ListGeneratedMailboxCandidates(context.Background(), 10, 2, domain.SupplyScopePublic, &bucket, 4, "")
	require.NoError(t, err)
	require.Empty(t, candidates)

	locked, err := repo.LockGeneratedMailboxCandidate(context.Background(), 2047, 4095, 10)
	require.NoError(t, err)
	require.Nil(t, locked)

	reusable, err := repo.FindReusableGeneratedMailbox(context.Background(), 10, 4095)
	require.NoError(t, err)
	require.NotNil(t, reusable)
	require.Equal(t, uint(2048), reusable.ID)
	require.Equal(t, uint(4095), reusable.ResourceID)

	require.NoError(t, db.Exec(`
UPDATE domain_allocations
SET status = 'released', released_at = NOW()
WHERE order_no = 'ord-active-generated'`).Error)
	reusable, err = repo.FindReusableGeneratedMailbox(context.Background(), 10, 4095)
	require.NoError(t, err)
	require.NotNil(t, reusable)
	require.Equal(t, uint(2048), reusable.ID)

	require.NoError(t, db.Exec(`
	UPDATE generated_mailboxes
	SET email = '__retired_2047@invalid.local', status = 'retired'
	WHERE id = 2047`).Error)
	require.NoError(t, db.Exec(`
	INSERT INTO generated_mailboxes(id, resource_id, owner_user_id, email, status, alloc_bucket)
	VALUES (2049, 4095, 1, 'active@d4095.example.com', 'normal', 2047)`).Error)
	require.NoError(t, db.Exec("UPDATE generated_mailboxes SET last_allocated_at = NOW() WHERE id = 2048").Error)

	candidates, err = repo.ListGeneratedMailboxCandidates(context.Background(), 10, 2, domain.SupplyScopePublic, &bucket, 4, "")
	require.NoError(t, err)
	require.Empty(t, candidates)
	reusable, err = repo.FindReusableGeneratedMailbox(context.Background(), 10, 4095)
	require.NoError(t, err)
	require.NotNil(t, reusable)
	require.Equal(t, uint(2048), reusable.ID)
	locked, err = repo.LockGeneratedMailboxCandidate(context.Background(), 2049, 4095, 10)
	require.NoError(t, err)
	require.Nil(t, locked)
	historicallyAllocated, err := repo.IsDomainEmailHistoricallyAllocated(context.Background(), 10, "ACTIVE@d4095.example.com")
	require.NoError(t, err)
	require.True(t, historicallyAllocated)

	locked, err = repo.LockGeneratedMailboxCandidate(context.Background(), 2049, 4095, 11)
	require.NoError(t, err)
	require.NotNil(t, locked)
}

func TestFindOrCreateMicrosoftAliasesDoNotReuseDisabledRowsMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 0, 1, 1)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	require.NoError(t, db.Exec(`
INSERT INTO dot_aliases(resource_id, email, status)
VALUES (1000, 'm.s1000@example.com', 'disabled')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO plus_aliases(resource_id, email, status)
VALUES (1000, 'ms1000+disabled@example.com', 'disabled')`).Error)

	repo := NewRepo(db)
	dot, err := repo.FindOrCreateDotAlias(context.Background(), 1000, "m.s1000@example.com")
	require.NoError(t, err)
	require.Nil(t, dot)
	plus, err := repo.FindOrCreatePlusAlias(context.Background(), 1000, "ms1000+disabled@example.com")
	require.NoError(t, err)
	require.Nil(t, plus)
}

func TestAllocationSQLConstraintsMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 2, true, "normal")
	seedDomainResources(t, db, 1, 2000, 2)
	require.NoError(t, db.Exec(`
	INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status)
	VALUES (2001, 1, 'wrong@d2001.example.com', 'normal')`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
		VALUES (1001, 1, 'alias1001@example.com', 'normal')`).Error)
	var explicitAliasID uint
	require.NoError(t, db.Raw("SELECT id FROM explicit_aliases WHERE resource_id = 1001").Scan(&explicitAliasID).Error)
	var mailboxID uint
	require.NoError(t, db.Raw("SELECT id FROM generated_mailboxes WHERE resource_id = 2001").Scan(&mailboxID).Error)

	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('ord-cross', 'microsoft')").Error)
	require.Error(t, db.Exec(`
	INSERT INTO domain_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox_id, email)
	VALUES ('ord-cross', 10, 20, 2000, 'public', ?, 'x@d2000.example.com')`, mailboxID).Error)

	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('ord-alias-mismatch', 'microsoft')").Error)
	require.Error(t, db.Exec(`
	INSERT INTO microsoft_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox, explicit_alias_id, email)
	VALUES ('ord-alias-mismatch', 10, 20, 1000, 'public', 'alias', ?, 'alias1001@example.com')`, explicitAliasID).Error)

	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('ord-mailbox-mismatch', 'domain')").Error)
	require.Error(t, db.Exec(`
	INSERT INTO domain_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox_id, email)
	VALUES ('ord-mailbox-mismatch', 10, 20, 2000, 'public', ?, 'wrong@d2001.example.com')`, mailboxID).Error)

	require.NoError(t, db.Exec("INSERT INTO projects(id, name, target_platform, status, access_type) VALUES (11, 'Other Project', 'alloc', 'listed', 'public')").Error)
	require.NoError(t, db.Exec(`
	INSERT INTO project_products(
	    id, project_id, type, status, code_enabled, purchase_enabled,
	    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
	    code_window_minutes, activation_window_minutes, warranty_minutes,
	    main_weight, dot_weight, plus_weight
	) VALUES (21, 11, 'microsoft', 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 1, 0, 0)`).Error)
	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('ord-product-mismatch', 'microsoft')").Error)
	require.Error(t, db.Exec(`
	INSERT INTO microsoft_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox, email)
	VALUES ('ord-product-mismatch', 10, 21, 1000, 'public', 'main', 'ms1000@example.com')`).Error)
}

func TestAllocateRollbackOnInsufficientInventoryMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)

	uc := allocapp.NewUseCase(NewRepo(db))
	_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-rollback",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	var guardCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM allocation_order_guards WHERE order_no = 'ord-rollback'").Scan(&guardCount).Error)
	require.Zero(t, guardCount)
}

func TestWithTxPanicRollsBackMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	repo := NewRepo(db)

	func() {
		defer func() {
			require.NotNil(t, recover())
		}()
		_ = repo.WithTx(context.Background(), func(txCtx context.Context) error {
			require.NoError(t, repo.CreateOrderGuard(txCtx, "ord-panic", domain.AllocationTypeMicrosoft))
			panic("rollback")
		})
	}()

	var guardCount int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM allocation_order_guards WHERE order_no = 'ord-panic'").Scan(&guardCount).Error)
	require.Zero(t, guardCount)
}

func TestInventoryStatsAreScopedToProjectProductsMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 2, true, "normal")
	seedDomainResources(t, db, 1, 2000, 3)

	repo := NewRepo(db)
	stats, err := repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.False(t, stats.Microsoft.Enabled)
	require.True(t, stats.Domain.Enabled)
	require.Equal(t, int64(3), stats.Domain.EligibleResources)
	require.Equal(t, int64(30000), stats.Domain.MailboxDailyLimit)
	require.Equal(t, int64(30000), stats.Domain.TotalAvailable)
	require.Equal(t, int64(30000), stats.TotalAvailable)
	productStats, err := repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, []allocapp.ProductInventorySuffixTotal{{
		Suffix: "com", TotalAvailable: 30000, PublicAvailable: 30000,
	}}, productStats.Items[0].Suffixes)

	_, err = repo.GetInventoryStats(context.Background(), 999)
	require.ErrorIs(t, err, domain.ErrProjectNotAllocatable)
}

func TestProjectInventoryAccessIsCheckedLiveMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	require.NoError(t, db.Exec("UPDATE projects SET access_type = 'private' WHERE id = 10").Error)
	repo := NewRepo(db)

	require.ErrorIs(t, repo.AssertProjectInventoryAccess(context.Background(), 10, 2), domain.ErrProjectNotAllocatable)
	require.NoError(t, db.Exec("INSERT INTO project_accesses(project_id, user_id, granted_by) VALUES (10, 2, 1)").Error)
	require.NoError(t, repo.AssertProjectInventoryAccess(context.Background(), 10, 2))
	require.NoError(t, db.Exec("DELETE FROM project_accesses WHERE project_id = 10 AND user_id = 2").Error)
	require.ErrorIs(t, repo.AssertProjectInventoryAccess(context.Background(), 10, 2), domain.ErrProjectNotAllocatable)
}

func TestUserProductInventoryIncludesOwnedPrivateDomainCapacityMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResources(t, db, 1, 1000, 1)
	seedDomainResourcesWithPurpose(t, db, 2, 2000, 3, "not_sale")
	seedDomainResourcesWithPurpose(t, db, 2, 4000, 1, "not_sale")
	seedDomainResourcesWithPurpose(t, db, 3, 3000, 1, "not_sale")
	require.NoError(t, db.Exec("UPDATE domain_resources SET mailbox_daily_limit = 1 WHERE id IN (2000, 2001)").Error)
	require.NoError(t, db.Exec("UPDATE domain_resources SET mailbox_daily_limit = 5 WHERE id = 2002").Error)
	require.NoError(t, db.Exec("UPDATE domain_resources SET mailbox_daily_limit = 2 WHERE id = 4000").Error)
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(id, resource_id, owner_user_id, email, status, alloc_bucket) VALUES
    (92000, 2000, 2, 'quota@d2000.example.com', 'normal', MOD(CRC32('quota@d2000.example.com'), 2048)),
    (92001, 2001, 2, 'available-a@d2001.example.com', 'normal', MOD(CRC32('available-a@d2001.example.com'), 2048)),
    (92002, 2001, 2, 'over-limit@d2001.example.com', 'normal', MOD(CRC32('over-limit@d2001.example.com'), 2048)),
    (92003, 2002, 2, 'used@d2002.example.com', 'normal', MOD(CRC32('used@d2002.example.com'), 2048)),
    (92004, 2002, 2, 'disabled@d2002.example.com', 'disabled', MOD(CRC32('disabled@d2002.example.com'), 2048)),
    (92005, 2002, 2, 'available-b@d2002.example.com', 'normal', MOD(CRC32('available-b@d2002.example.com'), 2048)),
    (93000, 3000, 3, 'other-owner@d3000.example.com', 'normal', MOD(CRC32('other-owner@d3000.example.com'), 2048))`).Error)
	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('ord-private-inventory-used', 'domain')").Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox_id, email)
VALUES ('ord-private-inventory-used', 10, 20, 2002, 'owned', 92003, 'used@d2002.example.com')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_daily_usages(usage_date, resource_type, resource_id, usage_kind, used_count)
VALUES (UTC_DATE(), 'domain', 2000, 'domain_mailbox', 1)`).Error)

	uc := allocapp.NewUseCase(NewRepo(db))
	totals, err := uc.GetProductInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Len(t, totals.Items, 1)
	require.Equal(t, int64(10008), totals.TotalAvailable)
	require.Equal(t, int64(10008), totals.Items[0].TotalAvailable)
	require.Equal(t, int64(10000), totals.Items[0].PublicAvailable)
	require.Equal(t, []allocapp.ProductInventorySuffixTotal{
		{Suffix: "com", TotalAvailable: 10000, PublicAvailable: 10000},
		{Suffix: "d2001.example.com", TotalAvailable: 1},
		{Suffix: "d2002.example.com", TotalAvailable: 5},
		{Suffix: "d4000.example.com", TotalAvailable: 2},
	}, totals.Items[0].Suffixes)
	for _, unavailable := range []string{
		"quota@d2000.example.com",
		"over-limit@d2001.example.com",
		"used@d2002.example.com",
		"disabled@d2002.example.com",
		"other-owner@d3000.example.com",
	} {
		for _, suffix := range totals.Items[0].Suffixes {
			require.NotEqual(t, unavailable, suffix.Suffix)
		}
	}

	allocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-private-domain", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScopes: []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic},
		EmailSuffix:  "d4000.example.com",
	})
	require.NoError(t, err)
	require.Equal(t, uint(4000), allocation.ResourceID)
	require.True(t, strings.HasSuffix(allocation.Email, "@d4000.example.com"))
}

func TestInventoryStatsExcludePrivateMicrosoftFromSharedPoolMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 2, 1000, 1, false, "normal")

	repo := NewRepo(db)
	stats, err := repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, stats.Microsoft.EligibleResources)
	require.Zero(t, stats.Microsoft.MainAvailable)
	require.Zero(t, stats.TotalAvailable)

	productStats, err := repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, uint(10), productStats.ProjectID)
	require.Zero(t, productStats.TotalAvailable)
	require.Len(t, productStats.Items, 1)
	require.Equal(t, uint(20), productStats.Items[0].ProductID)
	require.Zero(t, productStats.Items[0].TotalAvailable)
	require.Zero(t, productStats.Items[0].PublicAvailable)
	require.Empty(t, productStats.Items[0].Suffixes)

	userStats, err := allocapp.NewUseCase(repo).GetProductInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), userStats.TotalAvailable)
	require.Len(t, userStats.Items, 1)
	require.Equal(t, int64(1), userStats.Items[0].TotalAvailable)
	require.Zero(t, userStats.Items[0].PublicAvailable)
	require.Equal(t, []allocapp.ProductInventorySuffixTotal{{
		Suffix: "example.com", TotalAvailable: 1,
	}}, userStats.Items[0].Suffixes)
}

func TestICloudInventoryIgnoresExpirationAndCookieStateMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "icloud", 1, 0, 0)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES
    (1000, 'icloud', 1),
    (1001, 'icloud', 2)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resources(
	    id, primary_email, imap_app_password, expire_at, for_sale, status, alias_count
) VALUES
	    (1000, 'public@icloud.com', 'app-password', DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 DAY), TRUE, 'normal', 1),
	    (1001, 'owned@icloud.com', 'app-password', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), TRUE, 'normal', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resource_channels(resource_id, kind, host, cookie, session_status) VALUES
	    (1000, 'apple_account', 'appleid.apple.com', 'cookie', 'valid'),
	    (1001, 'icloud_web', 'p119-maildomainws.icloud.com', 'cookie', 'invalid')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_aliases(resource_id, anonymous_id, email, status) VALUES
    (1000, 'anon-public', 'public-alias@icloud.com', 'normal'),
    (1001, 'anon-owned', 'owned-alias@icloud.com', 'normal')`).Error)

	repo := NewRepo(db)
	stats, err := repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.ICloud.EligibleResources)
	require.Equal(t, int64(2), stats.ICloud.AliasAvailable)
	require.Equal(t, int64(2), stats.ICloud.TotalAvailable)

	totals, err := repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), totals.TotalAvailable)
	require.Equal(t, int64(2), totals.Items[0].PublicAvailable)

	candidates, err := repo.ListICloudSourceCandidates(
		context.Background(), 10, 2, domain.SupplyScopePublic, time.Now(), 10,
	)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	owned, err := repo.ListICloudSourceCandidates(
		context.Background(), 10, 2, domain.SupplyScopeOwned, time.Now(), 10,
	)
	require.NoError(t, err)
	require.Len(t, owned, 1)
	require.Equal(t, uint(1001), owned[0].ResourceID)

	var locked *allocapp.ICloudCandidate
	require.NoError(t, repo.WithTx(context.Background(), func(ctx context.Context) error {
		var lockErr error
		locked, lockErr = repo.LockICloudCandidate(
			ctx, candidates[0].ResourceID, candidates[0].AliasID, 10, 2,
			domain.SupplyScopePublic, time.Now(),
		)
		return lockErr
	}))
	require.NotNil(t, locked)

	require.NoError(t, db.Table("icloud_resources").Where("id = ?", 1001).Update("for_sale", false).Error)
	ownedTotals, err := repo.ListUserICloudInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, []allocapp.UserICloudInventoryTotal{{
		ProductID: 20, OwnedAvailable: 1, OwnedPublicAvailable: 0,
	}}, ownedTotals)
	owned, err = repo.ListICloudSourceCandidates(
		context.Background(), 10, 2, domain.SupplyScopeOwned, time.Now(), 10,
	)
	require.NoError(t, err)
	require.Len(t, owned, 1)
	require.Equal(t, uint(1001), owned[0].ResourceID)

	userTotals, err := allocapp.NewUseCase(repo).GetProductInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), userTotals.Items[0].TotalAvailable)
	require.Equal(t, int64(1), userTotals.Items[0].PublicAvailable)
}

func TestUserICloudInventoryTotalsAreOwnerScopedMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "icloud", 1, 0, 0)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES
    (1000, 'icloud', 1),
    (1001, 'icloud', 2),
    (1002, 'icloud', 2)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_resources(
	    id, primary_email, imap_app_password, expire_at, for_sale, status
) VALUES
	    (1000, 'other@icloud.com', 'app-password', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), TRUE, 'normal'),
	    (1001, 'owned-public@icloud.com', 'app-password', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), TRUE, 'normal'),
	    (1002, 'owned-private@icloud.com', 'app-password', DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY), FALSE, 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO icloud_aliases(resource_id, anonymous_id, email, status) VALUES
    (1000, 'anon-0', 'other-alias@icloud.com', 'normal'),
    (1001, 'anon-1', 'owned-public-alias@icloud.com', 'normal'),
    (1002, 'anon-2', 'owned-private-alias@icloud.com', 'normal')`).Error)

	repo := NewRepo(db)
	rows, err := repo.ListUserICloudInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, []allocapp.UserICloudInventoryTotal{{
		ProductID: 20, OwnedAvailable: 2, OwnedPublicAvailable: 1,
	}}, rows)

	totals, err := allocapp.NewUseCase(repo).GetProductInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.EqualValues(t, 3, totals.TotalAvailable)
	require.EqualValues(t, 3, totals.Items[0].TotalAvailable)
	require.EqualValues(t, 2, totals.Items[0].PublicAvailable)
}

func TestInventoryStatsExcludeReleasedProjectMainAndAliasHistoryMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	repo := NewRepo(db)
	uc := allocapp.NewUseCase(repo)

	assertInventory := func(main, aliases, total int64) {
		t.Helper()
		stats, err := repo.GetInventoryStats(context.Background(), 10)
		require.NoError(t, err)
		require.Equal(t, main, stats.Microsoft.MainAvailable)
		require.Equal(t, aliases, stats.Microsoft.ExplicitAliasAvailable)
		require.Equal(t, total, stats.TotalAvailable)

		products, err := repo.GetProductInventoryTotals(context.Background(), 10)
		require.NoError(t, err)
		require.Equal(t, total, products.TotalAvailable)
		require.Len(t, products.Items, 1)
		require.Equal(t, total, products.Items[0].TotalAvailable)
		require.Equal(t, total, products.Items[0].PublicAvailable)
		if total == 0 {
			require.Empty(t, products.Items[0].Suffixes)
		} else {
			require.Equal(t, []allocapp.ProductInventorySuffixTotal{{
				Suffix: "example.com", TotalAvailable: total, PublicAvailable: total,
			}}, products.Items[0].Suffixes)
		}
	}

	assertInventory(1, 0, 1)
	mainAllocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-inventory-main-history", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "main", mainAllocation.Mailbox)
	_, err = uc.ReleaseByOrder(context.Background(), mainAllocation.OrderNo)
	require.NoError(t, err)
	assertInventory(0, 0, 0)

	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (1000, 1, 'available-alias@example.com', 'normal')`).Error)
	assertInventory(0, 1, 1)

	aliasAllocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-inventory-alias-history", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "alias", aliasAllocation.Mailbox)
	_, err = uc.ReleaseByOrder(context.Background(), aliasAllocation.OrderNo)
	require.NoError(t, err)
	assertInventory(0, 0, 0)
}

func TestMicrosoftExplicitAliasUsesOwnSuffixForInventoryAndAllocationMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET email_address = 'primary@outlook.com', email_domain = 'outlook.com'
WHERE id = 1000`).Error)

	repo := NewRepo(db)
	uc := allocapp.NewUseCase(repo)
	main, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-cross-suffix-main", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic, EmailSuffix: "outlook.com",
	})
	require.NoError(t, err)
	require.Equal(t, "primary@outlook.com", main.Email)
	_, err = uc.ReleaseByOrder(context.Background(), main.OrderNo)
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status) VALUES
    (1000, 1, 'first@hotmail.com', 'normal'),
    (1000, 1, 'second@outlook.com', 'normal')`).Error)

	totals, err := repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, []allocapp.ProductInventorySuffixTotal{
		{Suffix: "hotmail.com", TotalAvailable: 1, PublicAvailable: 1},
		{Suffix: "outlook.com", TotalAvailable: 1, PublicAvailable: 1},
	}, totals.Items[0].Suffixes)

	hotmailAllocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-cross-suffix-hotmail-alias", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic, EmailSuffix: "hotmail.com",
	})
	require.NoError(t, err)
	require.Equal(t, "alias", hotmailAllocation.Mailbox)
	require.Equal(t, "first@hotmail.com", hotmailAllocation.Email)
	_, err = uc.ReleaseByOrder(context.Background(), hotmailAllocation.OrderNo)
	require.NoError(t, err)

	totals, err = repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, []allocapp.ProductInventorySuffixTotal{
		{Suffix: "outlook.com", TotalAvailable: 1, PublicAvailable: 1},
	}, totals.Items[0].Suffixes)

	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-cross-suffix-hotmail-exhausted", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic, EmailSuffix: "hotmail.com",
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	allocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-cross-suffix-outlook-alias", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic, EmailSuffix: "@outlook.com",
	})
	require.NoError(t, err)
	require.Equal(t, "alias", allocation.Mailbox)
	require.Equal(t, "second@outlook.com", allocation.Email)
}

func TestMicrosoftSplitCandidatesPreserveOrderingMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 2, true, "normal")
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET email_address = CASE id
        WHEN 1000 THEN 'low@hotmail.com'
        ELSE 'primary@outlook.com'
    END,
    email_domain = CASE id
        WHEN 1000 THEN 'hotmail.com'
        ELSE 'outlook.com'
    END,
    quality_score = CASE id
        WHEN 1000 THEN 10
        ELSE 90
    END
WHERE id IN (1000, 1001)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (1001, 1, 'high@hotmail.com', 'normal')`).Error)

	candidates, err := NewRepo(db).ListMicrosoftSourceCandidates(
		context.Background(), 10, 2, domain.SupplyScopePublic,
		domain.MicrosoftMailboxMain, nil, 2, "hotmail.com",
	)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	require.Equal(t, []uint{1001, 1000}, []uint{candidates[0].ResourceID, candidates[1].ResourceID})
}

type candidateSQLCapture struct {
	gormlogger.Interface
	queries []string
}

func (l *candidateSQLCapture) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sqlText, rows := fc()
	if strings.Contains(sqlText, "ORDER BY ms.last_allocated_at ASC, ms.quality_score DESC, ms.id ASC") {
		l.queries = append(l.queries, sqlText)
	}
	l.Interface.Trace(ctx, begin, func() (string, int64) { return sqlText, rows }, err)
}

func TestMicrosoftCandidateFullPlansShowBucketBoundAndGlobalSuffixScanMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	const resources = 2048
	seedMicrosoftResources(t, db, 1, 1000, resources, true, "normal")

	type aliasSeed struct {
		ResourceID  uint `gorm:"column:resource_id"`
		OwnerUserID uint `gorm:"column:owner_user_id"`
		Email       string
		Status      string
	}
	type matchSeed struct {
		ResourceID    uint      `gorm:"column:resource_id"`
		ProjectID     uint      `gorm:"column:project_id"`
		FirstMatched  time.Time `gorm:"column:first_matched_at"`
		LastMatched   time.Time `gorm:"column:last_matched_at"`
		EvidenceCount uint      `gorm:"column:evidence_count"`
		LastScanned   time.Time `gorm:"column:last_scanned_at"`
	}
	now := time.Now().UTC()
	aliases := make([]aliasSeed, resources)
	matches := make([]matchSeed, resources)
	for i := range resources {
		resourceID := uint(1000 + i)
		aliases[i] = aliasSeed{
			ResourceID: resourceID, OwnerUserID: 1,
			Email: fmt.Sprintf("alias%d@example.com", resourceID), Status: "normal",
		}
		matches[i] = matchSeed{
			ResourceID: resourceID, ProjectID: 10, FirstMatched: now, LastMatched: now,
			EvidenceCount: 1, LastScanned: now,
		}
	}
	require.NoError(t, db.Table("explicit_aliases").CreateInBatches(aliases, 1000).Error)
	require.NoError(t, db.Table("microsoft_resource_project_matches").CreateInBatches(matches, 1000).Error)
	require.NoError(t, db.Exec("ANALYZE TABLE microsoft_resources, explicit_aliases, microsoft_resource_project_matches").Error)

	capture := &candidateSQLCapture{Interface: db.Logger}
	loggedDB := db.Session(&gorm.Session{Logger: capture})
	repo := NewRepo(loggedDB)
	bucket := coredomain.MicrosoftAllocationBucket(1000)
	for _, selectedBucket := range []*uint16{&bucket, nil} {
		candidates, err := repo.ListMicrosoftSourceCandidates(
			context.Background(), 10, 2, domain.SupplyScopePublic,
			domain.MicrosoftMailboxMain, selectedBucket, 8, "example.com",
		)
		require.NoError(t, err)
		require.Empty(t, candidates)
	}
	require.Len(t, capture.queries, 4, "main and alias SQL must be captured for bucket and global fallback")

	bucketMain, bucketAlias := capture.queries[0], capture.queries[1]
	globalMain, globalAlias := capture.queries[2], capture.queries[3]
	require.Contains(t, bucketMain, "FORCE INDEX (idx_microsoft_suffix_bucket)")
	require.Contains(t, bucketMain, "ms.alloc_bucket =")
	require.Contains(t, bucketAlias, "FORCE INDEX (idx_explicit_aliases_suffix_bucket)")
	require.Contains(t, bucketAlias, "ea.alloc_bucket =")
	require.NotContains(t, globalMain, "FORCE INDEX")
	require.NotContains(t, globalMain, "ms.alloc_bucket =")
	require.NotContains(t, globalAlias, "FORCE INDEX")
	require.NotContains(t, globalAlias, "ea.alloc_bucket =")

	requireExplainTargetUsesIndex(t, db, bucketMain, "ms", "idx_microsoft_suffix_bucket")
	requireExplainTargetUsesIndex(t, db, bucketAlias, "ea", "idx_explicit_aliases_suffix_bucket")
	requireExplainTargetUsesIndex(t, db, globalMain, "ms", "")
	requireExplainTargetUsesIndex(t, db, globalAlias, "ea", "")

	bucketMainWork := explainAnalyzeTargetWork(t, db, bucketMain, "ms")
	bucketAliasWork := explainAnalyzeTargetWork(t, db, bucketAlias, "ea")
	globalMainWork := explainAnalyzeTargetWork(t, db, globalMain, "ms")
	globalAliasWork := explainAnalyzeTargetWork(t, db, globalAlias, "ea")
	require.LessOrEqual(t, bucketMainWork, float64(2))
	require.LessOrEqual(t, bucketAliasWork, float64(2))
	require.GreaterOrEqual(t, globalMainWork, float64(resources))
	require.GreaterOrEqual(t, globalAliasWork, float64(resources))
}

func TestDotInventoryCountsOnlyDistinctAllocatableVariantsMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 0, 1, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	require.NoError(t, db.Exec(`
UPDATE microsoft_resources
SET email_address = 'm.s1000@example.com'
WHERE id = 1000`).Error)
	repo := NewRepo(db)
	uc := allocapp.NewUseCase(repo)
	wantAliases := []string{
		"m.s.1000@example.com",
		"m.s1.000@example.com",
		"m.s10.00@example.com",
		"m.s100.0@example.com",
	}
	productTotals, err := repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(len(wantAliases)), productTotals.TotalAvailable)

	for allocated := int64(0); allocated < int64(len(wantAliases)); allocated++ {
		stats, err := repo.GetInventoryStats(context.Background(), 10)
		require.NoError(t, err)
		require.Equal(t, int64(len(wantAliases)), stats.Microsoft.DotCapacity)
		require.Equal(t, int64(len(wantAliases))-allocated, stats.Microsoft.DotAvailable)
		allocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
			OrderNo: fmt.Sprintf("ord-dot-inventory-%d", allocated), BuyerUserID: 2,
			ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
		})
		require.NoError(t, err)
		require.Equal(t, "dot", allocation.Mailbox)
		require.Equal(t, wantAliases[allocated], allocation.Email)
		_, err = uc.ReleaseByOrder(context.Background(), allocation.OrderNo)
		require.NoError(t, err)
	}

	stats, err := repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, stats.Microsoft.DotAvailable)
	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-dot-inventory-exhausted", BuyerUserID: 2,
		ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	require.NoError(t, db.Exec(`
INSERT INTO dot_aliases(resource_id, email, status)
VALUES
    (1000, 'm..s1000@example.com', 'normal'),
    (1000, 'imported-history-shape@example.com', 'normal')`).Error)
	stats, err = repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, stats.Microsoft.DotAvailable)
	productTotals, err = repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, productTotals.TotalAvailable)
	reusable, err := repo.FindReusableDotAlias(context.Background(), 10, 1000)
	require.NoError(t, err)
	require.Nil(t, reusable)

	require.NoError(t, db.Exec(`
INSERT INTO dot_aliases(resource_id, email, status)
VALUES (1000, 'm.s1.0.00@example.com', 'normal')`).Error)
	stats, err = repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.Microsoft.DotAvailable)
	productTotals, err = repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), productTotals.TotalAvailable)
	allocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-dot-inventory-imported", BuyerUserID: 2,
		ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "m.s1.0.00@example.com", allocation.Email)
}

func TestInventoryStatsExcludePrivateDomainFromSharedPoolMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResourcesWithPurpose(t, db, 2, 2000, 1, "not_sale")

	repo := NewRepo(db)
	stats, err := repo.GetInventoryStats(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, stats.Domain.EligibleResources)
	require.Zero(t, stats.Domain.TotalAvailable)
	require.Zero(t, stats.TotalAvailable)

	productStats, err := repo.GetProductInventoryTotals(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, productStats.TotalAvailable)
	require.Len(t, productStats.Items, 1)
	require.Zero(t, productStats.Items[0].TotalAvailable)
	require.Zero(t, productStats.Items[0].PublicAvailable)
	require.Empty(t, productStats.Items[0].Suffixes)
}

func TestPlusDailyLimitConsumesPerResourceCounterMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 0, 0, 1)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET plus_daily_limit = 1 WHERE id = 1000").Error)

	uc := allocapp.NewUseCase(NewRepo(db))
	first, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-plus-limit-1",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.Equal(t, "plus", first.Mailbox)

	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-plus-limit-2",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	var used int
	require.NoError(t, db.Raw(`
SELECT used_count
FROM allocation_daily_usages
WHERE resource_type = 'microsoft' AND resource_id = 1000 AND usage_kind = 'plus'`).Scan(&used).Error)
	require.Equal(t, 1, used)
}

func TestPlusDailyLimitConcurrentMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 0, 0, 1)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	const dailyLimit = 5
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET plus_daily_limit = ? WHERE id = 1000", dailyLimit).Error)

	uc := allocapp.NewUseCase(NewRepo(db))
	const workers = 20
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
				OrderNo:          fmt.Sprintf("ord-plus-concurrent-%03d", i),
				BuyerUserID:      2,
				ProjectProductID: 20,
				SupplyScope:      domain.SupplyScopePublic,
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)

	successes := 0
	insufficient := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrInsufficientInventory):
			insufficient++
		default:
			require.NoError(t, err)
		}
	}
	require.Positive(t, successes)
	require.LessOrEqual(t, successes, dailyLimit)
	require.Equal(t, workers-successes, insufficient)

	var used int
	require.NoError(t, db.Raw(`
SELECT used_count
FROM allocation_daily_usages
WHERE resource_type = 'microsoft' AND resource_id = 1000 AND usage_kind = 'plus'`).Scan(&used).Error)
	require.Equal(t, successes, used)

	var active int
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM microsoft_allocations
WHERE resource_id = 1000 AND mailbox = 'plus' AND status = 'allocated'`).Scan(&active).Error)
	require.Equal(t, successes, active)

	for i := successes; i < dailyLimit; i++ {
		_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
			OrderNo:          fmt.Sprintf("ord-plus-topup-%03d", i),
			BuyerUserID:      2,
			ProjectProductID: 20,
			SupplyScope:      domain.SupplyScopePublic,
		})
		require.NoError(t, err)
	}

	_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-plus-over-limit",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	require.NoError(t, db.Raw(`
SELECT used_count
FROM allocation_daily_usages
WHERE resource_type = 'microsoft' AND resource_id = 1000 AND usage_kind = 'plus'`).Scan(&used).Error)
	require.Equal(t, dailyLimit, used)
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM microsoft_allocations
WHERE resource_id = 1000 AND mailbox = 'plus' AND status = 'allocated'`).Scan(&active).Error)
	require.Equal(t, dailyLimit, active)
}

func TestDomainDailyLimitConsumesPerResourceCounterMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResources(t, db, 1, 2000, 1)
	require.NoError(t, db.Exec("UPDATE domain_resources SET mailbox_daily_limit = 1 WHERE id = 2000").Error)

	uc := allocapp.NewUseCase(NewRepo(db))
	_, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-domain-concrete-suffix",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
		EmailSuffix:      "d2000.example.com",
	})
	require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)

	first, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-domain-limit-1",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
		EmailSuffix:      "com",
	})
	require.NoError(t, err)
	require.Equal(t, domain.AllocationTypeDomain, first.Type)

	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-domain-limit-2",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	var used int
	require.NoError(t, db.Raw(`
SELECT used_count
FROM allocation_daily_usages
WHERE resource_type = 'domain' AND resource_id = 2000 AND usage_kind = 'domain_mailbox'`).Scan(&used).Error)
	require.Equal(t, 1, used)
}

func TestOwnedDomainSelectionUsesExactMailboxMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResourcesWithPurpose(t, db, 2, 2000, 2, "not_sale")
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status, alloc_bucket) VALUES
    (2001, 2, 'other@d2001.example.com', 'normal', MOD(CRC32('other@d2001.example.com'), 2048)),
    (2001, 2, 'chosen@d2001.example.com', 'normal', MOD(CRC32('chosen@d2001.example.com'), 2048))`).Error)

	uc := allocapp.NewUseCase(NewRepo(db))
	allocation, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-domain-exact", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScopes: []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic},
		EmailSuffix:  "chosen@d2001.example.com",
	})
	require.NoError(t, err)
	require.Equal(t, uint(2001), allocation.ResourceID)
	require.Equal(t, "chosen@d2001.example.com", allocation.Email)

	var mailboxCount int64
	require.NoError(t, db.Table("generated_mailboxes").Where("resource_id = ?", 2001).Count(&mailboxCount).Error)
	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-domain-missing", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScopes: []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic},
		EmailSuffix:  "missing@d2001.example.com",
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)
	var mailboxCountAfter int64
	require.NoError(t, db.Table("generated_mailboxes").Where("resource_id = ?", 2001).Count(&mailboxCountAfter).Error)
	require.Equal(t, mailboxCount, mailboxCountAfter)

	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-domain-other-owner", BuyerUserID: 3, ProjectProductID: 20,
		SupplyScopes: []domain.SupplyScope{domain.SupplyScopeOwned, domain.SupplyScopePublic},
		EmailSuffix:  "other@d2001.example.com",
	})
	require.ErrorIs(t, err, domain.ErrInsufficientInventory)

	_, err = uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "ord-domain-public-private", BuyerUserID: 2, ProjectProductID: 20,
		SupplyScope: domain.SupplyScopePublic, EmailSuffix: "other@d2001.example.com",
	})
	require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
}

func TestAllocationMigrationIndexesAndExplainMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 16, true, "normal")
	seedDomainResources(t, db, 1, 2000, 16)
	seedDomainResourcesWithPurpose(t, db, 1, 3000, 16, "not_sale")
	seedDomainResourcesWithPurpose(t, db, 2, 4000, 64, "not_sale")
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status, last_allocated_at)
VALUES
    (2000, 1, 'a@d2000.example.com', 'normal', NULL),
    (2000, 1, 'b@d2000.example.com', 'normal', NOW())`).Error)
	var activeMailboxID uint
	require.NoError(t, db.Raw("SELECT MIN(id) FROM generated_mailboxes WHERE resource_id = 2000").Scan(&activeMailboxID).Error)
	require.NoError(t, db.Exec("INSERT INTO allocation_order_guards(order_no, type) VALUES ('ord-explain-domain-active', 'domain')").Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_allocations(order_no, project_id, product_id, resource_id, supply_scope, mailbox_id, email)
VALUES ('ord-explain-domain-active', 10, 20, 2000, 'public', ?, 'a@d2000.example.com')`, activeMailboxID).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_daily_usages(usage_date, resource_type, resource_id, usage_kind, used_count)
VALUES (CURRENT_DATE(), 'microsoft', 1000, 'plus', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO explicit_aliases(resource_id, owner_user_id, email, status)
VALUES (1000, 1, 'indexed@example.com', 'normal')`).Error)
	require.NoError(t, db.Exec("ANALYZE TABLE microsoft_resources, explicit_aliases, domain_resources").Error)

	for _, item := range []struct {
		table string
		index string
	}{
		{"microsoft_resources", "idx_microsoft_alloc_public"},
		{"microsoft_resources", "idx_microsoft_alloc_owned"},
		{"microsoft_resources", "idx_microsoft_inventory_public"},
		{"microsoft_resources", "idx_microsoft_suffix_bucket"},
		{"domain_resources", "idx_domain_alloc_public"},
		{"domain_resources", "idx_domain_alloc_owned"},
		{"domain_resources", "idx_domain_alloc_tld_public"},
		{"domain_resources", "idx_domain_inventory_public"},
		{"domain_resources", "idx_domain_resources_owner_tld_private"},
		{"project_products", "idx_project_products_id_project"},
		{"explicit_aliases", "idx_explicit_aliases_id_resource"},
		{"explicit_aliases", "idx_explicit_aliases_alloc_reuse"},
		{"explicit_aliases", "idx_explicit_aliases_suffix_bucket"},
		{"dot_aliases", "idx_dot_aliases_id_resource"},
		{"dot_aliases", "idx_dot_aliases_alloc_reuse"},
		{"plus_aliases", "idx_plus_aliases_id_resource"},
		{"plus_aliases", "idx_plus_aliases_alloc_reuse"},
		{"generated_mailboxes", "idx_generated_mailboxes_id_resource"},
		{"generated_mailboxes", "idx_generated_mailboxes_alloc_reuse"},
		{"generated_mailboxes", "idx_generated_mailboxes_bucket_reuse"},
		{"allocation_daily_usages", "PRIMARY"},
		{"allocation_daily_usages", "idx_allocation_daily_usages_resource"},
		{"allocation_order_guards", "PRIMARY"},
		{"allocation_order_guards", "idx_allocation_order_guards_order_type"},
		{"microsoft_allocations", "idx_ms_alloc_active"},
		{"microsoft_allocations", "idx_ms_alloc_guard_type"},
		{"microsoft_allocations", "idx_ms_alloc_product_project"},
		{"microsoft_allocations", "idx_ms_alloc_explicit_alias_resource"},
		{"microsoft_allocations", "idx_ms_alloc_dot_alias_resource"},
		{"microsoft_allocations", "idx_ms_alloc_plus_alias_resource"},
		{"microsoft_allocations", "idx_ms_alloc_resource_mailbox_created"},
		{"microsoft_allocations", "idx_ms_alloc_resource_created_id"},
		{"microsoft_allocations", "idx_ms_alloc_email_status"},
		{"microsoft_allocations", "idx_ms_alloc_resource_project_mailbox"},
		{"microsoft_allocations", "idx_ms_alloc_explicit_project_mailbox"},
		{"microsoft_allocations", "idx_ms_alloc_dot_project_mailbox"},
		{"microsoft_allocations", "idx_ms_alloc_plus_project_mailbox"},
		{"domain_allocations", "idx_domain_alloc_active_mailbox"},
		{"domain_allocations", "idx_domain_alloc_guard_type"},
		{"domain_allocations", "idx_domain_alloc_product_project"},
		{"domain_allocations", "idx_domain_alloc_mailbox_resource"},
		{"domain_allocations", "idx_domain_alloc_resource_created"},
		{"domain_allocations", "idx_domain_alloc_email_status"},
		{"domain_allocations", "idx_domain_alloc_email_project"},
	} {
		requireIndexExists(t, db, item.table, item.index)
	}

	requireExplainUsesIndex(t, db,
		"idx_microsoft_alloc_public",
		"EXPLAIN SELECT id FROM microsoft_resources WHERE alloc_bucket = MOD(1000, 2048) AND for_sale = TRUE AND status = 'normal' ORDER BY last_allocated_at ASC, quality_score DESC, id ASC LIMIT 4",
	)
	requireExplainUsesIndex(t, db,
		"idx_microsoft_suffix_bucket",
		"EXPLAIN SELECT id FROM microsoft_resources FORCE INDEX (idx_microsoft_suffix_bucket) WHERE email_domain = 'example.com' AND alloc_bucket = MOD(1000, 2048) LIMIT 4",
	)
	requireExplainUsesIndex(t, db,
		"idx_explicit_aliases_suffix_bucket",
		"EXPLAIN SELECT resource_id FROM explicit_aliases FORCE INDEX (idx_explicit_aliases_suffix_bucket) WHERE email_domain = 'example.com' AND alloc_bucket = MOD(1000, 2048) AND status = 'normal' LIMIT 4",
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_alloc_public",
		"EXPLAIN SELECT id FROM domain_resources WHERE alloc_bucket = MOD(2000, 512) AND purpose = 'sale' AND status = 'normal' ORDER BY last_allocated_at ASC, id ASC LIMIT 4",
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_alloc_tld_public",
		"EXPLAIN SELECT id FROM domain_resources WHERE domain_tld = '.com' AND purpose = 'sale' AND status = 'normal' ORDER BY last_allocated_at ASC, id ASC LIMIT 8",
		20,
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_resources_owner_tld_private",
		"EXPLAIN SELECT id FROM domain_resources WHERE owner_user_id = 1 AND domain_tld = '.com' AND purpose = 'not_sale' AND status = 'normal' ORDER BY last_allocated_at ASC, id ASC LIMIT 8",
		20,
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_inventory_public",
		"EXPLAIN SELECT domain_tld, SUM(mailbox_daily_limit) FROM domain_resources WHERE purpose = 'sale' AND status = 'normal' GROUP BY domain_tld",
		20,
	)
	requireExplainUsesIndex(t, db,
		"idx_generated_mailboxes_bucket_reuse",
		"EXPLAIN SELECT id FROM generated_mailboxes WHERE alloc_bucket = MOD(2000, 2048) AND status = 'normal' ORDER BY last_allocated_at ASC, id ASC LIMIT 4",
	)
	requireExplainUsesIndex(t, db,
		"idx_generated_mailboxes_alloc_reuse",
		"EXPLAIN SELECT id FROM generated_mailboxes WHERE resource_id = 2000 AND status = 'normal' ORDER BY last_allocated_at ASC, id ASC LIMIT 1",
	)
	requireExplainUsesIndex(t, db,
		"idx_ms_alloc_email_status",
		"EXPLAIN SELECT id FROM microsoft_allocations WHERE email = 'ms1000@example.com' AND status = 'allocated'",
	)
	requireExplainUsesIndex(t, db,
		"idx_ms_alloc_resource_created_id",
		"EXPLAIN SELECT id, order_no FROM microsoft_allocations WHERE resource_id = 1000 ORDER BY created_at DESC, id DESC LIMIT 20",
	)
	requireExplainUsesIndex(t, db,
		"idx_ms_alloc_resource_project_mailbox",
		"EXPLAIN SELECT 1 FROM microsoft_allocations FORCE INDEX (idx_ms_alloc_resource_project_mailbox) WHERE resource_id = 1000 AND project_id = 10 AND mailbox = 'main' LIMIT 1",
	)
	requireExplainUsesIndex(t, db,
		"idx_ms_alloc_explicit_project_mailbox",
		"EXPLAIN SELECT 1 FROM microsoft_allocations FORCE INDEX (idx_ms_alloc_explicit_project_mailbox) WHERE explicit_alias_id = 1 AND project_id = 10 AND mailbox = 'alias' LIMIT 1",
	)
	requireExplainUsesIndex(t, db,
		"idx_ms_alloc_dot_project_mailbox",
		"EXPLAIN SELECT 1 FROM microsoft_allocations FORCE INDEX (idx_ms_alloc_dot_project_mailbox) WHERE dot_alias_id = 1 AND project_id = 10 AND mailbox = 'dot' LIMIT 1",
	)
	requireExplainUsesIndex(t, db,
		"idx_ms_alloc_plus_project_mailbox",
		"EXPLAIN SELECT 1 FROM microsoft_allocations FORCE INDEX (idx_ms_alloc_plus_project_mailbox) WHERE plus_alias_id = 1 AND project_id = 10 AND mailbox = 'plus' LIMIT 1",
	)
	requireExplainUsesIndex(t, db,
		"PRIMARY",
		"EXPLAIN SELECT used_count FROM allocation_daily_usages WHERE usage_date = CURRENT_DATE() AND resource_type = 'microsoft' AND resource_id = 1000 AND usage_kind = 'plus' FOR UPDATE",
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_alloc_active_mailbox",
		fmt.Sprintf("EXPLAIN SELECT 1 FROM domain_allocations FORCE INDEX (idx_domain_alloc_active_mailbox) WHERE active_project_id = 10 AND active_mailbox_id = %d LIMIT 1", activeMailboxID),
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_alloc_email_project",
		"EXPLAIN SELECT 1 FROM domain_allocations FORCE INDEX (idx_domain_alloc_email_project) WHERE email = 'a@d2000.example.com' AND project_id = 10 LIMIT 1",
	)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, allocMigrationsDir(t), 92))
	requireIndexMissing(t, db, "microsoft_resources", "idx_microsoft_suffix_bucket")
	requireIndexMissing(t, db, "explicit_aliases", "idx_explicit_aliases_suffix_bucket")
	require.False(t, db.Migrator().HasColumn("explicit_aliases", "email_domain"))
	require.False(t, db.Migrator().HasColumn("explicit_aliases", "alloc_bucket"))
	require.NoError(t, goose.UpTo(sqlDB, allocMigrationsDir(t), 93))
	requireIndexExists(t, db, "microsoft_resources", "idx_microsoft_suffix_bucket")
	requireIndexMissing(t, db, "explicit_aliases", "idx_explicit_aliases_suffix_bucket")
	require.False(t, db.Migrator().HasColumn("explicit_aliases", "email_domain"))
	require.NoError(t, goose.UpTo(sqlDB, allocMigrationsDir(t), 94))
	require.True(t, db.Migrator().HasColumn("explicit_aliases", "email_domain"))
	require.True(t, db.Migrator().HasColumn("explicit_aliases", "alloc_bucket"))
	requireIndexMissing(t, db, "explicit_aliases", "idx_explicit_aliases_suffix_bucket")
	require.NoError(t, goose.UpTo(sqlDB, allocMigrationsDir(t), 95))
	requireIndexExists(t, db, "explicit_aliases", "idx_explicit_aliases_suffix_bucket")
}

func seedAllocBase(t *testing.T, db *gorm.DB, productType string, mainWeight, dotWeight, plusWeight int) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, status, role) VALUES
	    (1, 'super-admin@test.local', 'hash', 'super-admin', 'active', 'super_admin'),
	    (2, 'buyer@test.local', 'hash', 'buyer', 'active', 'user'),
	    (3, 'regular@test.local', 'hash', 'regular', 'active', 'user'),
	    (4, 'operator@test.local', 'hash', 'operator', 'active', 'admin')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, name, target_platform, status, access_type, loose_match)
VALUES (10, 'Alloc Project', 'alloc', 'listed', 'public', TRUE)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (?, 10, ?, 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, ?, ?, ?)`,
		20, productType, mainWeight, dotWeight, plusWeight).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES
    (10, 'sender', '.*', TRUE),
    (10, 'recipient', 'exact', TRUE)`).Error)
}

func seedMicrosoftResources(t *testing.T, db *gorm.DB, ownerID, startID, count int, forSale bool, status string) {
	t.Helper()
	type emailResourceSeed struct {
		ID          int
		Type        string
		OwnerUserID int `gorm:"column:owner_user_id"`
	}
	type microsoftResourceSeed struct {
		ID           int
		EmailAddress string `gorm:"column:email_address"`
		EmailDomain  string `gorm:"column:email_domain"`
		Password     string
		ForSale      bool `gorm:"column:for_sale"`
		Status       string
		QualityScore int    `gorm:"column:quality_score"`
		AllocBucket  uint16 `gorm:"column:alloc_bucket"`
	}

	roots := make([]emailResourceSeed, count)
	resources := make([]microsoftResourceSeed, count)
	for i := 0; i < count; i++ {
		id := startID + i
		qualityScore := 100 - i
		if qualityScore < 0 {
			qualityScore = 0
		}
		roots[i] = emailResourceSeed{ID: id, Type: "microsoft", OwnerUserID: ownerID}
		resources[i] = microsoftResourceSeed{
			ID:           id,
			EmailAddress: fmt.Sprintf("ms%d@example.com", id),
			EmailDomain:  "example.com",
			Password:     "secret",
			ForSale:      forSale,
			Status:       status,
			QualityScore: qualityScore,
			AllocBucket:  coredomain.MicrosoftAllocationBucket(uint(id)),
		}
	}
	require.NoError(t, db.Table("email_resources").CreateInBatches(roots, 1000).Error)
	require.NoError(t, db.Table("microsoft_resources").CreateInBatches(resources, 1000).Error)
}

type gmailResourceSeed struct {
	ID          int
	OwnerUserID int
	Email       string
	ForSale     bool
}

func seedGmailResources(t *testing.T, db *gorm.DB, items []gmailResourceSeed) {
	t.Helper()
	for _, item := range items {
		require.NoError(t, db.Exec(
			"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'gmail', ?)", item.ID, item.OwnerUserID,
		).Error)
		require.NoError(t, db.Exec(`
INSERT INTO gmail_resources(
    id, resource_type, owner_user_id, email, identity, password,
    two_factor_secret, app_password, for_sale, status, alloc_bucket
) VALUES (?, 'gmail', ?, ?, ?, 'password', 'JBSWY3DPEHPK3PXP', 'abcdefghijklmnop', ?, 'normal', MOD(?, 2048))`,
			item.ID, item.OwnerUserID, item.Email, item.Email, item.ForSale, item.ID,
		).Error)
	}
}

func requireIndexExists(t *testing.T, db *gorm.DB, tableName string, indexName string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		tableName,
		indexName,
	).Scan(&count).Error)
	require.Positive(t, count, "expected index %s on %s", indexName, tableName)
}

func requireIndexMissing(t *testing.T, db *gorm.DB, tableName string, indexName string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		tableName,
		indexName,
	).Scan(&count).Error)
	require.Zero(t, count, "unexpected index %s on %s", indexName, tableName)
}

func requireExplainUsesIndex(t *testing.T, db *gorm.DB, expectedKey string, query string, maxRows ...int64) {
	t.Helper()
	rowLimit := int64(10)
	if len(maxRows) > 0 {
		rowLimit = maxRows[0]
	}

	var rows []struct {
		Key        sql.NullString `gorm:"column:key"`
		Rows       sql.NullInt64  `gorm:"column:rows"`
		AccessType sql.NullString `gorm:"column:type"`
	}
	require.NoError(t, db.Raw(query).Scan(&rows).Error)
	require.NotEmpty(t, rows, "expected EXPLAIN rows for %s", query)
	seenKeys := make([]string, 0, len(rows))
	usedExpectedKey := false
	for _, row := range rows {
		require.True(t, row.Key.Valid, "expected query to use an index: %s", query)
		seenKeys = append(seenKeys, row.Key.String)
		require.True(t, row.Rows.Valid, "expected query to expose row estimate: %s", query)
		require.LessOrEqual(t, row.Rows.Int64, rowLimit, "unexpected row estimate for %s using %s", query, row.Key.String)
		require.NotEqual(t, "ALL", row.AccessType.String, "unexpected full table scan for %s", query)
		if row.Key.String == expectedKey {
			usedExpectedKey = true
		}
	}
	require.True(t, usedExpectedKey, "expected query to use index %s, saw %v: %s", expectedKey, seenKeys, query)
}

func requireExplainTargetUsesIndex(t *testing.T, db *gorm.DB, query, targetTable, expectedKey string) {
	t.Helper()
	var rows []struct {
		Table      sql.NullString `gorm:"column:table"`
		Key        sql.NullString `gorm:"column:key"`
		Rows       sql.NullInt64  `gorm:"column:rows"`
		AccessType sql.NullString `gorm:"column:type"`
		Extra      sql.NullString `gorm:"column:Extra"`
	}
	require.NoError(t, db.Raw("EXPLAIN "+query).Scan(&rows).Error)
	for _, row := range rows {
		if !row.Table.Valid || row.Table.String != targetTable {
			continue
		}
		require.True(t, row.Key.Valid, "expected %s to use an index: %s", targetTable, query)
		require.NotEqual(t, "ALL", row.AccessType.String, "unexpected full table scan on %s: %s", targetTable, query)
		if expectedKey != "" {
			require.Equal(t, expectedKey, row.Key.String, "unexpected index on %s: %s", targetTable, query)
		}
		t.Logf("EXPLAIN target=%s key=%s rows=%d extra=%s", targetTable, row.Key.String, row.Rows.Int64, row.Extra.String)
		return
	}
	require.Failf(t, "target table missing from EXPLAIN", "target=%s query=%s", targetTable, query)
}

var explainActualRowsPattern = regexp.MustCompile(`rows=([0-9.eE+-]+) loops=([0-9]+)\)\s*$`)

func explainAnalyzeTargetWork(t *testing.T, db *gorm.DB, query, targetTable string) float64 {
	t.Helper()
	rows, err := db.Raw("EXPLAIN ANALYZE " + query).Rows()
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	work := float64(0)
	plan := make([]string, 0)
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan = append(plan, line)
		if !strings.Contains(line, " on "+targetTable) {
			continue
		}
		match := explainActualRowsPattern.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		actualRows, parseErr := strconv.ParseFloat(match[1], 64)
		require.NoError(t, parseErr)
		loops, parseErr := strconv.ParseFloat(match[2], 64)
		require.NoError(t, parseErr)
		work = max(work, actualRows*loops)
	}
	require.NoError(t, rows.Err())
	require.Positive(t, work, "missing actual rows/loops for %s in plan:\n%s", targetTable, strings.Join(plan, "\n"))
	t.Logf("EXPLAIN ANALYZE target=%s work=%.0f\n%s", targetTable, work, strings.Join(plan, "\n"))
	return work
}

func seedDomainResources(t *testing.T, db *gorm.DB, ownerID, startID, count int) {
	seedDomainResourcesWithPurpose(t, db, ownerID, startID, count, "sale")
}

func seedDomainResourcesWithPurpose(t *testing.T, db *gorm.DB, ownerID, startID, count int, purpose string) {
	t.Helper()
	mailServerID := 900 + ownerID
	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, name, server_address, mx_record, status)
VALUES (?, ?, 'default', 'mx.aishop6.com', 'mx.aishop6.com', 'online')
ON DUPLICATE KEY UPDATE status = VALUES(status)`, mailServerID, ownerID).Error)
	for i := 0; i < count; i++ {
		id := startID + i
		domainName := fmt.Sprintf("d%d.example.com", id)
		require.NoError(t, db.Exec(
			"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'domain', ?)",
			id,
			ownerID,
		).Error)
		require.NoError(t, db.Exec(`
INSERT INTO domain_resources(id, resource_type, owner_user_id, domain, domain_tld, mail_server_id, purpose, status, alloc_bucket)
VALUES (?, 'domain', ?, ?, '.com', ?, ?, 'normal', MOD(?, 512))`,
			id,
			ownerID,
			domainName,
			mailServerID,
			purpose,
			id,
		).Error)
	}
}
