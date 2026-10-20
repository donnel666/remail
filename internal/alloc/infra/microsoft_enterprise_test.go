package infra

import (
	"context"
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftEnterpriseInventoryAndAllocationIsolationMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "microsoft_enterprise", 1, 0, 0)
	require.NoError(t, db.Exec(`INSERT INTO project_products(id,project_id,type,status,code_enabled,code_price,code_window_minutes,main_weight) VALUES(21,10,'microsoft','enabled',TRUE,1,10,1)`).Error)
	seedMicrosoftResources(t, db, 1, 1001, 1, true, "normal")
	require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(1000,'microsoft_enterprise',1)").Error)
	require.NoError(t, db.Exec("INSERT INTO microsoft_enterprise_resources(id,email_address,email_domain,for_sale,status,alloc_bucket) VALUES(1000,'user@tenant.onmicrosoft.com','tenant.onmicrosoft.com',TRUE,'normal',1000)").Error)
	require.NoError(t, db.Exec("UPDATE microsoft_resources SET email_address='user@outlook.com',email_domain='outlook.com' WHERE id=1001").Error)
	repo := NewRepo(db)
	ctx := context.Background()
	totals, err := repo.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, totals.TotalAvailable)
	require.Len(t, totals.Items, 2)
	for _, item := range totals.Items {
		require.EqualValues(t, 1, item.TotalAvailable)
		require.Len(t, item.Suffixes, 1)
	}
	for _, enterprise := range []bool{true, false} {
		candidates, err := repo.ListMicrosoftSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, domain.MicrosoftMailboxMain, []uint16{1000, 1001}, 10, "", enterprise)
		require.NoError(t, err)
		require.Len(t, candidates, 1)
		require.Equal(t, enterprise, coredomain.IsMicrosoftEnterpriseEmail(candidates[0].EmailAddress))
	}
	wrong, err := repo.LockMicrosoftCandidate(ctx, 1000, 10, 2, domain.SupplyScopePublic, domain.MicrosoftMailboxMain, "", false)
	require.NoError(t, err)
	require.Nil(t, wrong)
	require.NoError(t, db.Exec("INSERT INTO mailmatch_resource_fetch_states(email_resource_id,operation_kind,status) VALUES(1000,'enterprise_history','pending')").Error)
	blocked, err := repo.ListMicrosoftSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, domain.MicrosoftMailboxMain, []uint16{1000}, 10, "", true)
	require.NoError(t, err)
	require.Empty(t, blocked, "enterprise history identification blocks allocation on its own lifecycle")
	blockedInventory, err := repo.ListProductSuffixInventory(ctx, allocapp.ProductAllocationConfig{ProjectID: 10, ProductType: coredomain.ProductTypeMicrosoftEnterprise, MainWeight: 1}, 2, domain.SupplyScopePublic)
	require.NoError(t, err)
	require.Empty(t, blockedInventory)
	require.NoError(t, db.Exec("UPDATE mailmatch_resource_fetch_states SET status='normal' WHERE email_resource_id=1000").Error)
	uc := allocapp.NewUseCase(repo)
	allocation, err := uc.Allocate(ctx, allocapp.AllocateCommand{OrderNo: testmysql.AllocationOrderNo("enterprise", 10, "main", 1000, coredomain.MicrosoftAllocationBucketCount), BuyerUserID: 2, ProjectProductID: 20, SupplyScope: domain.SupplyScopePublic})
	require.NoError(t, err)
	require.Equal(t, "user@tenant.onmicrosoft.com", allocation.Email)
	require.Equal(t, domain.AllocationTypeMicrosoftEnterprise, allocation.Type)
	require.Equal(t, "main", allocation.Mailbox)
}
