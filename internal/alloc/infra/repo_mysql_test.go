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
    (1000, 4, 'used-alias@example.com', 'normal'),
    (1000, 4, 'free-alias@example.com', 'normal')`).Error)
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
	require.ErrorIs(t, err, domain.ErrAllocationConflict)
	require.Nil(t, mailbox)
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
		VALUES (1001, 4, 'alias1001@example.com', 'normal')`).Error)
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
	stats, err := repo.GetInventoryStats(context.Background(), 10, 0)
	require.NoError(t, err)
	require.False(t, stats.Microsoft.Enabled)
	require.True(t, stats.Domain.Enabled)
	require.Equal(t, int64(3), stats.Domain.EligibleResources)
	require.Equal(t, int64(30000), stats.Domain.MailboxDailyLimit)
	require.Equal(t, int64(30000), stats.Domain.TotalAvailable)
	require.Equal(t, int64(30000), stats.TotalAvailable)

	_, err = repo.GetInventoryStats(context.Background(), 999, 0)
	require.ErrorIs(t, err, domain.ErrProjectNotAllocatable)
}

func TestInventoryStatsIncludeBuyerPrivateMicrosoftMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 2, 1000, 1, false, "normal")

	repo := NewRepo(db)
	adminStats, err := repo.GetInventoryStats(context.Background(), 10, 0)
	require.NoError(t, err)
	require.Zero(t, adminStats.Microsoft.EligibleResources)
	require.Zero(t, adminStats.TotalAvailable)

	buyerStats, err := repo.GetInventoryStats(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), buyerStats.Microsoft.EligibleResources)
	require.Equal(t, int64(1), buyerStats.Microsoft.MainAvailable)
	require.Equal(t, int64(1), buyerStats.TotalAvailable)

	productStats, err := repo.GetProductInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, uint(10), productStats.ProjectID)
	require.Equal(t, int64(1), productStats.TotalAvailable)
	require.Len(t, productStats.Items, 1)
	require.Equal(t, uint(20), productStats.Items[0].ProductID)
	require.Equal(t, int64(1), productStats.Items[0].TotalAvailable)
}

func TestInventoryStatsIncludeBuyerPrivateDomainMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "domain", 0, 0, 0)
	seedDomainResourcesWithPurpose(t, db, 2, 2000, 1, "not_sale")

	repo := NewRepo(db)
	adminStats, err := repo.GetInventoryStats(context.Background(), 10, 0)
	require.NoError(t, err)
	require.Zero(t, adminStats.Domain.EligibleResources)
	require.Zero(t, adminStats.TotalAvailable)

	buyerStats, err := repo.GetInventoryStats(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), buyerStats.Domain.EligibleResources)
	require.Equal(t, int64(10000), buyerStats.Domain.TotalAvailable)
	require.Equal(t, int64(10000), buyerStats.TotalAvailable)

	productStats, err := repo.GetProductInventoryTotals(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Equal(t, int64(10000), productStats.TotalAvailable)
	require.Len(t, productStats.Items, 1)
	require.Equal(t, int64(10000), productStats.Items[0].TotalAvailable)
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
		"PRIMARY",
		"EXPLAIN SELECT used_count FROM allocation_daily_usages WHERE usage_date = CURRENT_DATE() AND resource_type = 'microsoft' AND resource_id = 1000 AND usage_kind = 'plus' FOR UPDATE",
	)
}

func seedAllocBase(t *testing.T, db *gorm.DB, productType string, mainWeight, dotWeight, plusWeight int) {
	t.Helper()
	require.NoError(t, db.Exec(`
INSERT INTO users(id, email, password_hash, nickname, enabled, role) VALUES
	    (1, 'supplier@test.local', 'hash', 'supplier', TRUE, 'supplier'),
	    (2, 'buyer@test.local', 'hash', 'buyer', TRUE, 'user'),
	    (3, 'regular@test.local', 'hash', 'regular', TRUE, 'user'),
	    (4, 'alias-owner@test.local', 'hash', 'alias-owner', TRUE, 'super_admin')`).Error)
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
