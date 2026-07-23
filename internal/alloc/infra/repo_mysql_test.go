package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var allocMySQLTestServer = testmysql.New("remail_alloc_test")

func TestMain(m *testing.M) {
	code := m.Run()
	_ = allocMySQLTestServer.Close(context.Background())
	os.Exit(code)
}

func newAllocMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return allocMySQLTestServer.Database(t, allocMigrationsDir(t))
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
	seedMicrosoftResources(t, db, 1, 1000, 150, true, "normal")
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

func TestCandidateRefreshUsesProjectStateAndGenerationFenceMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 1, true, "normal")
	repo := NewRepo(db)
	ctx := context.Background()

	state, err := repo.RequestCandidateRefresh(ctx, 10, 4, "request-1", "/projects/10/candidates/refresh")
	require.NoError(t, err)
	require.Equal(t, domain.CandidateRefreshPending, state.Status)
	require.Equal(t, uint64(1), state.Generation)
	require.Zero(t, state.Failures)

	pending, err := repo.ListPendingCandidateRefreshes(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, uint(10), pending[0].ProjectID)
	processing, err := repo.MarkCandidateRefreshProcessing(ctx, 10, state.Generation)
	require.NoError(t, err)
	require.True(t, processing)

	released, err := repo.ReleaseCandidateRefreshInfrastructureFailure(ctx, 10, state.Generation, "database unavailable")
	require.NoError(t, err)
	require.True(t, released)
	pending, err = repo.ListPendingCandidateRefreshes(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, uint64(2), pending[0].Generation)
	require.Zero(t, pending[0].Failures, "infrastructure failure must not consume the business budget")

	generation := pending[0].Generation
	for failure := 1; failure <= 3; failure++ {
		processing, err = repo.MarkCandidateRefreshProcessing(ctx, 10, generation)
		require.NoError(t, err)
		require.True(t, processing)
		recorded, abnormal, err := repo.RecordCandidateRefreshFailure(ctx, 10, generation, "refresh rejected")
		require.NoError(t, err)
		require.True(t, recorded)
		require.Equal(t, failure == 3, abnormal)
		generation++
	}

	var terminal CandidateRefreshProjectModel
	require.NoError(t, db.First(&terminal, 10).Error)
	require.Equal(t, string(domain.CandidateRefreshAbnormal), terminal.Status)
	require.Equal(t, 3, terminal.Failures)
	require.Equal(t, generation, terminal.Generation)

	retriggered, err := repo.RequestCandidateRefresh(ctx, 10, 4, "request-2", "/projects/10/candidates/refresh")
	require.NoError(t, err)
	require.Equal(t, domain.CandidateRefreshPending, retriggered.Status)
	require.Zero(t, retriggered.Failures)
	require.Equal(t, generation+1, retriggered.Generation)
	processing, err = repo.MarkCandidateRefreshProcessing(ctx, 10, retriggered.Generation)
	require.NoError(t, err)
	require.True(t, processing)

	newer, err := repo.RequestCandidateRefresh(ctx, 10, 4, "request-3", "/projects/10/candidates/refresh")
	require.NoError(t, err)
	_, current, err := repo.RunCandidateRefresh(ctx, 10, retriggered.Generation)
	require.NoError(t, err)
	require.False(t, current, "an old generation must not execute after a retrigger")
	processing, err = repo.MarkCandidateRefreshProcessing(ctx, 10, newer.Generation)
	require.NoError(t, err)
	require.True(t, processing)
	affected, current, err := repo.RunCandidateRefresh(ctx, 10, newer.Generation)
	require.NoError(t, err)
	require.True(t, current)
	require.Equal(t, 1, affected)
	require.NoError(t, db.First(&terminal, 10).Error)
	require.Equal(t, string(domain.CandidateRefreshNormal), terminal.Status)

	var legacyTableCount int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name = 'allocation_candidate_refresh_jobs'`).Scan(&legacyTableCount).Error)
	require.Zero(t, legacyTableCount)
	requireIndexExists(t, db, "projects", "idx_projects_candidate_refresh_pending")
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
	first, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-domain-limit-1",
		BuyerUserID:      2,
		ProjectProductID: 20,
		SupplyScope:      domain.SupplyScopePublic,
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

func TestAllocationMigrationIndexesAndExplainMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 16, true, "normal")
	seedDomainResources(t, db, 1, 2000, 16)
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(resource_id, owner_user_id, email, status, last_allocated_at)
VALUES
    (2000, 1, 'a@d2000.example.com', 'normal', NULL),
    (2000, 1, 'b@d2000.example.com', 'normal', NOW())`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_daily_usages(usage_date, resource_type, resource_id, usage_kind, used_count)
VALUES (CURRENT_DATE(), 'microsoft', 1000, 'plus', 1)`).Error)

	for _, item := range []struct {
		table string
		index string
	}{
		{"microsoft_resources", "idx_microsoft_alloc_public"},
		{"microsoft_resources", "idx_microsoft_alloc_owned"},
		{"microsoft_resources", "idx_microsoft_inventory_public"},
		{"domain_resources", "idx_domain_alloc_public"},
		{"domain_resources", "idx_domain_alloc_owned"},
		{"domain_resources", "idx_domain_inventory_public"},
		{"project_products", "idx_project_products_id_project"},
		{"explicit_aliases", "idx_explicit_aliases_id_resource"},
		{"explicit_aliases", "idx_explicit_aliases_alloc_reuse"},
		{"dot_aliases", "idx_dot_aliases_id_resource"},
		{"dot_aliases", "idx_dot_aliases_alloc_reuse"},
		{"plus_aliases", "idx_plus_aliases_id_resource"},
		{"plus_aliases", "idx_plus_aliases_alloc_reuse"},
		{"generated_mailboxes", "idx_generated_mailboxes_id_resource"},
		{"generated_mailboxes", "idx_generated_mailboxes_alloc_reuse"},
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
	} {
		requireIndexExists(t, db, item.table, item.index)
	}

	requireExplainUsesIndex(t, db,
		"idx_microsoft_alloc_public",
		"EXPLAIN SELECT id FROM microsoft_resources WHERE alloc_bucket = MOD(1000, 64) AND for_sale = TRUE AND status = 'normal' ORDER BY last_allocated_at ASC, quality_score DESC, id ASC LIMIT 4",
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_alloc_public",
		"EXPLAIN SELECT id FROM domain_resources WHERE alloc_bucket = MOD(2000, 64) AND purpose = 'sale' AND status = 'normal' ORDER BY last_allocated_at ASC, id ASC LIMIT 4",
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
	for i := 0; i < count; i++ {
		id := startID + i
		email := fmt.Sprintf("ms%d@example.com", id)
		qualityScore := 100 - i
		if qualityScore < 0 {
			qualityScore = 0
		}
		require.NoError(t, db.Exec(
			"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'microsoft', ?)",
			id,
			ownerID,
		).Error)
		require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, email_address, email_domain, password, for_sale, status, quality_score, alloc_bucket)
VALUES (?, ?, 'example.com', 'secret', ?, ?, ?, MOD(?, 64))`,
			id,
			email,
			forSale,
			status,
			qualityScore,
			id,
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

func requireExplainUsesIndex(t *testing.T, db *gorm.DB, expectedKey string, query string) {
	t.Helper()

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
		require.LessOrEqual(t, row.Rows.Int64, int64(10), "unexpected row estimate for %s using %s", query, row.Key.String)
		require.NotEqual(t, "ALL", row.AccessType.String, "unexpected full table scan for %s", query)
		if row.Key.String == expectedKey {
			usedExpectedKey = true
		}
	}
	require.True(t, usedExpectedKey, "expected query to use index %s, saw %v: %s", expectedKey, seenKeys, query)
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
VALUES (?, 'domain', ?, ?, 'example.com', ?, ?, 'normal', MOD(?, 64))`,
			id,
			ownerID,
			domainName,
			mailServerID,
			purpose,
			id,
		).Error)
	}
}
