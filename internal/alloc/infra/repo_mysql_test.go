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

func TestReleaseMakesMicrosoftMainReusableMySQL(t *testing.T) {
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
	second, err := uc.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo:          "ord-release-2",
		BuyerUserID:      2,
		ProjectProductID: 20,
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

func TestRefreshRoutingCandidatesMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_price, purchase_price, code_supplier_price, purchase_supplier_price,
    code_window_minutes, activation_window_minutes, warranty_minutes,
    main_weight, dot_weight, plus_weight
) VALUES (21, 10, 'domain', 'enabled', TRUE, FALSE, 1, 0, 0.5, 0, 10, 60, 60, 0, 0, 0)`).Error)
	seedMicrosoftResources(t, db, 1, 1000, 3, true, "normal")
	seedMicrosoftResources(t, db, 3, 2000, 2, true, "normal")
	seedDomainResources(t, db, 1, 3000, 2)
	seedDomainResourcesWithPurpose(t, db, 2, 4000, 1, "not_sale")

	repo := NewRepo(db)
	affected, err := repo.RefreshRoutingCandidates(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 6, affected)

	result, err := repo.ListRoutingCandidates(context.Background(), allocapp.CandidateFilter{ProjectID: 10, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(6), result.Total)
	require.Len(t, result.Items, 6)

	result, err = repo.ListRoutingCandidates(context.Background(), allocapp.CandidateFilter{ProjectID: 10, Type: domain.AllocationTypeMicrosoft, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(3), result.Total)
	require.Len(t, result.Items, 3)
	require.Equal(t, domain.AllocationTypeMicrosoft, result.Items[0].Type)

	result, err = repo.ListRoutingCandidates(context.Background(), allocapp.CandidateFilter{ProjectID: 10, Type: domain.AllocationTypeDomain, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(3), result.Total)
	require.Len(t, result.Items, 3)
	require.Equal(t, domain.AllocationTypeDomain, result.Items[0].Type)
	var privateDomainCandidateFound bool
	for _, item := range result.Items {
		if item.ResourceID == 4000 {
			privateDomainCandidateFound = true
			require.False(t, item.ForSale)
		}
	}
	require.True(t, privateDomainCandidateFound)
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
	INSERT INTO explicit_aliases(resource_id, email, status)
	VALUES (1001, 'alias1001@example.com', 'normal')`).Error)
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

func TestCandidateRefreshJobLifecycleMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	seedMicrosoftResources(t, db, 1, 1000, 2, true, "normal")
	repo := NewRepo(db)

	job := &domain.CandidateRefreshJob{
		ProjectID:      10,
		OperatorUserID: 1,
		Status:         domain.CandidateRefreshPending,
		RequestID:      "req-refresh",
		Path:           "/v1/admin/projects/:projectId/candidates/refresh",
	}
	created, err := repo.CreateCandidateRefreshJobWithLog(context.Background(), job)
	require.NoError(t, err)
	require.True(t, created)
	require.NotZero(t, job.ID)
	require.Equal(t, domain.CandidateRefreshPending, job.Status)

	duplicate := &domain.CandidateRefreshJob{ProjectID: 10, OperatorUserID: 1}
	created, err = repo.CreateCandidateRefreshJobWithLog(context.Background(), duplicate)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, job.ID, duplicate.ID)

	queued, err := repo.MarkCandidateRefreshJobQueued(context.Background(), job.ID)
	require.NoError(t, err)
	require.True(t, queued)
	running, err := repo.MarkCandidateRefreshJobRunning(context.Background(), job.ID)
	require.NoError(t, err)
	require.True(t, running)
	affected, err := repo.RefreshRoutingCandidates(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 2, affected)
	require.NoError(t, repo.MarkCandidateRefreshJobSucceeded(context.Background(), job.ID, affected))

	stored, err := repo.FindCandidateRefreshJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, domain.CandidateRefreshSucceeded, stored.Status)
	require.Equal(t, 2, stored.Affected)
}

func TestStaleRunningCandidateRefreshJobExpiresMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	repo := NewRepo(db)
	old := time.Now().UTC().Add(-30 * time.Minute)
	require.NoError(t, db.Exec(`
INSERT INTO allocation_candidate_refresh_jobs(
    project_id, operator_user_id, status, attempts, max_attempts,
    request_id, path, started_at, created_at, updated_at
) VALUES (10, 1, 'running', 1, 1, 'req-stale', '/v1/admin/projects/:projectId/candidates/refresh', ?, ?, ?)`,
		old, old, old).Error)
	var staleID uint
	require.NoError(t, db.Raw("SELECT id FROM allocation_candidate_refresh_jobs WHERE request_id = 'req-stale'").Scan(&staleID).Error)

	expired, err := repo.ExpireStaleCandidateRefreshJobs(context.Background(), time.Now().UTC().Add(-10*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, expired)

	stored, err := repo.FindCandidateRefreshJob(context.Background(), staleID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, domain.CandidateRefreshFailed, stored.Status)
	require.NotNil(t, stored.FinishedAt)
	require.Equal(t, "Candidate refresh job expired before completion.", stored.LastSafeError)

	next := &domain.CandidateRefreshJob{ProjectID: 10, OperatorUserID: 1}
	created, err := repo.CreateCandidateRefreshJobWithLog(context.Background(), next)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, staleID, next.ID)
	require.Equal(t, domain.CandidateRefreshPending, next.Status)
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
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET plus_daily_limit = 5 WHERE id = 1000").Error)

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
	require.Equal(t, 5, successes)
	require.Equal(t, workers-5, insufficient)

	var used int
	require.NoError(t, db.Raw(`
SELECT used_count
FROM allocation_daily_usages
WHERE resource_type = 'microsoft' AND resource_id = 1000 AND usage_kind = 'plus'`).Scan(&used).Error)
	require.Equal(t, 5, used)
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
		{"allocation_candidate_refresh_jobs", "idx_alloc_refresh_active_project"},
		{"allocation_candidate_refresh_jobs", "idx_alloc_refresh_status_updated"},
		{"allocation_daily_usages", "PRIMARY"},
		{"allocation_daily_usages", "idx_allocation_daily_usages_resource"},
		{"microsoft_routing_candidates", "idx_ms_candidates_project_bucket"},
		{"domain_routing_candidates", "idx_domain_candidates_project_bucket"},
		{"domain_routing_candidates", "idx_domain_candidates_project_resource"},
		{"allocation_order_guards", "PRIMARY"},
		{"allocation_order_guards", "idx_allocation_order_guards_order_type"},
		{"microsoft_allocations", "idx_ms_alloc_active_main"},
		{"microsoft_allocations", "idx_ms_alloc_active_alias"},
		{"microsoft_allocations", "idx_ms_alloc_active_dot"},
		{"microsoft_allocations", "idx_ms_alloc_active_plus"},
		{"microsoft_allocations", "idx_ms_alloc_guard_type"},
		{"microsoft_allocations", "idx_ms_alloc_product_project"},
		{"microsoft_allocations", "idx_ms_alloc_explicit_alias_resource"},
		{"microsoft_allocations", "idx_ms_alloc_dot_alias_resource"},
		{"microsoft_allocations", "idx_ms_alloc_plus_alias_resource"},
		{"microsoft_allocations", "idx_ms_alloc_resource_mailbox_created"},
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
		"idx_alloc_refresh_status_updated",
		"EXPLAIN SELECT id FROM allocation_candidate_refresh_jobs WHERE status = 'pending' ORDER BY id ASC LIMIT 100",
	)
	requireExplainUsesIndex(t, db,
		"idx_domain_candidates_project_bucket",
		"EXPLAIN SELECT id FROM domain_routing_candidates WHERE project_id = 10 AND alloc_bucket = MOD(2000, 64) AND status = 'normal' AND purpose = 'sale' ORDER BY last_allocated_at ASC, resource_id ASC LIMIT 4",
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
    (3, 'regular@test.local', 'hash', 'regular', TRUE, 'user')`).Error)
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
			100-i,
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
