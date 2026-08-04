package gmail

import (
	"context"
	"testing"
	"time"

	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailPurchaseSellsOneResourceAndReturnsOnlyToOrderLookup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-purchase?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{}))
	prepareLocalGmailAllocationTestSchema(t, db)
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "purchase@gmail.com", Identity: "purchase@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", ForSale: true, Status: localResourceRollbackNormal,
	}).Error)

	service := NewService(db, nil)
	delivery, err := service.AllocateLocalPurchase(context.Background(), "PURCHASE-1", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly, tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"})
	require.NoError(t, err)
	require.Equal(t, "purchase@gmail.com", delivery.Email)
	require.Equal(t, "password", delivery.Password)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, localResourceRollbackNormal, resource.Status, "allocation occupancy must not overwrite resource health")
	var allocation allocationModel
	require.NoError(t, db.Where("order_no = ?", "PURCHASE-1").First(&allocation).Error)
	require.EqualValues(t, 11, allocation.ProjectID)
	require.EqualValues(t, 12, allocation.ProductID)
	require.Equal(t, AllocationStatusAllocated, allocation.Status)
	require.NoError(t, service.releaseLocalCodeAllocation(context.Background(), "PURCHASE-1"))
	require.NoError(t, db.First(&allocation, allocation.ID).Error)
	require.Equal(t, AllocationStatusAllocated, allocation.Status, "purchase allocations are permanent")
	stored, err := service.FindLocalPurchase(context.Background(), "PURCHASE-1")
	require.NoError(t, err)
	require.Equal(t, delivery, stored)
	replayed, err := service.AllocateLocalPurchase(context.Background(), "PURCHASE-1", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly, tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"})
	require.NoError(t, err)
	require.Equal(t, delivery, replayed)
	_, err = service.AllocateLocalPurchase(context.Background(), "PURCHASE-2", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly, tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"})
	require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory)
	require.NoError(t, service.ReleaseLocalAllocation(context.Background(), "PURCHASE-1"))
	require.NoError(t, db.First(&allocation, allocation.ID).Error)
	require.Equal(t, AllocationStatusReleased, allocation.Status)
}

func TestLocalGmailPurchaseRechecksProjectHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-purchase-history?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{}))
	prepareLocalGmailAllocationTestSchema(t, db)
	resources := make([]localResourceModel, 2)
	for i, email := range []string{"history@gmail.com", "fresh@gmail.com"} {
		root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
		require.NoError(t, db.Create(&root).Error)
		resources[i] = localResourceModel{
			ID: root.ID, ResourceType: "gmail", OwnerUserID: 1, Email: email, Identity: email,
			Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
			ForSale: true, Status: LocalResourceNormal,
		}
		require.NoError(t, db.Create(&resources[i]).Error)
	}
	require.NoError(t, db.Create(&localAllocationGuardModel{OrderNo: "HISTORY-1", Type: "gmail"}).Error)
	historyResourceID := resources[0].ID
	releasedAt := time.Now().UTC()
	require.NoError(t, db.Create(&allocationModel{
		OrderNo: "HISTORY-1", GuardType: "gmail", ProjectID: 11, ProductID: 12,
		Source: SourceLocal, ServiceMode: string(tradedomain.ServiceModeCode), ResourceID: &historyResourceID,
		SupplyScope: AllocationSupplyPublic, Email: resources[0].Email, Status: AllocationStatusReleased,
		ReleasedAt: &releasedAt,
	}).Error)

	delivery, err := NewService(db, nil).AllocateLocalPurchase(
		context.Background(), "PURCHASE-AFTER-HISTORY", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly,
		tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"},
	)
	require.NoError(t, err)
	require.Equal(t, resources[1].ID, delivery.ResourceID)
}

func TestLocalGmailAllocationHonorsPrivateFirstAndPublicOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-allocation-policy?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{}))
	prepareLocalGmailAllocationTestSchema(t, db)
	require.NoError(t, db.Exec("INSERT INTO users(id, status, role) VALUES (2, 'active', 'user')").Error)

	privateRoot := resourceRootModel{Type: "gmail", OwnerUserID: 2}
	publicRoot := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&privateRoot).Error)
	require.NoError(t, db.Create(&publicRoot).Error)
	for _, resource := range []localResourceModel{
		{ID: privateRoot.ID, ResourceType: "gmail", OwnerUserID: 2, Email: "private@gmail.com", Identity: "private@gmail.com", Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", ForSale: false, Status: LocalResourceNormal},
		{ID: publicRoot.ID, ResourceType: "gmail", OwnerUserID: 1, Email: "public@gmail.com", Identity: "public@gmail.com", Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", ForSale: true, Status: LocalResourceNormal},
	} {
		require.NoError(t, db.Create(&resource).Error)
	}

	service := NewService(db, nil)
	owned, err := service.AllocateLocalCode(
		context.Background(), "GMAIL-PRIVATE-FIRST", 2, 11, 12, tradedomain.SupplyPolicyPrivateFirst,
		tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "7"},
	)
	require.NoError(t, err)
	require.Equal(t, privateRoot.ID, uint(*mustLocalGmailAllocation(t, db, owned.AllocationID).ResourceID))
	require.Equal(t, tradeapp.SupplyScopeOwned, owned.SupplyScope)
	require.Equal(t, "0.00", mustLocalGmailAllocation(t, db, owned.AllocationID).CostPointsSnapshot)

	public, err := service.AllocateLocalCode(
		context.Background(), "GMAIL-PUBLIC-ONLY", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly,
		tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "7"},
	)
	require.NoError(t, err)
	require.Equal(t, publicRoot.ID, uint(*mustLocalGmailAllocation(t, db, public.AllocationID).ResourceID))
	require.Equal(t, tradeapp.SupplyScopePublic, public.SupplyScope)
}

func TestLocalGmailAllocationRechecksProductModeAndPublicOwner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-allocation-recheck?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{}))
	prepareLocalGmailAllocationTestSchema(t, db)
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1, Email: "guarded@gmail.com", Identity: "guarded@gmail.com",
		Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", ForSale: true, Status: LocalResourceNormal,
	}).Error)
	service := NewService(db, nil)

	require.NoError(t, db.Exec("UPDATE project_products SET purchase_enabled = 0 WHERE id = 12").Error)
	_, err = service.AllocateLocalPurchase(
		context.Background(), "GMAIL-MODE-DISABLED", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly,
		tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "1"},
	)
	require.ErrorIs(t, err, allocdomain.ErrProjectNotAllocatable)

	require.NoError(t, db.Exec("UPDATE users SET role = 'user' WHERE id = 1").Error)
	_, err = service.AllocateLocalCode(
		context.Background(), "GMAIL-INELIGIBLE-OWNER", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly,
		tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "1"},
	)
	require.ErrorIs(t, err, tradedomain.ErrInsufficientInventory)
}

func TestLocalGmailDotAndPlusAllocationKeepsAliasHistoryPerProject(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-local-alias-allocation?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{}))
	prepareLocalGmailAllocationTestSchema(t, db)
	require.NoError(t, db.Exec(`
INSERT INTO projects(id, status, access_type) VALUES (21, 'listed', 'public')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    main_weight, dot_weight, plus_weight
) VALUES (22, 21, 'gmail', 'enabled', 1, 1, 0, 1, 0)`).Error)
	require.NoError(t, db.Exec(`
UPDATE project_products SET main_weight = 0, dot_weight = 1, plus_weight = 0 WHERE id = 12`).Error)

	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "firstname@gmail.com", Identity: "firstname@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", ForSale: true, Status: LocalResourceNormal,
	}).Error)
	service := NewService(db, nil)
	allocate := func(orderNo string, projectID, productID uint) allocationModel {
		result, err := service.AllocateLocalCode(
			context.Background(), orderNo, 2, projectID, productID, tradedomain.SupplyPolicyPublicOnly,
			tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"},
		)
		require.NoError(t, err)
		allocation := mustLocalGmailAllocation(t, db, result.AllocationID)
		require.Equal(t, GmailMailboxDot, allocation.Mailbox)
		require.NotEqual(t, "firstname@gmail.com", allocation.Email)
		return allocation
	}

	first := allocate("GMAIL-DOT-FIRST", 11, 12)
	require.NoError(t, service.ReleaseLocalAllocation(context.Background(), first.OrderNo))
	second := allocate("GMAIL-DOT-SECOND", 11, 12)
	require.NotEqual(t, first.Email, second.Email, "one project must not reuse the same dot alias")
	require.NoError(t, service.ReleaseLocalAllocation(context.Background(), second.OrderNo))
	otherProject := allocate("GMAIL-DOT-OTHER-PROJECT", 21, 22)
	require.Equal(t, first.Email, otherProject.Email, "another project may reuse the same Gmail dot alias")

	require.NoError(t, service.ReleaseLocalAllocation(context.Background(), otherProject.OrderNo))
	require.NoError(t, db.Exec(`
UPDATE project_products SET main_weight = 0, dot_weight = 0, plus_weight = 1 WHERE id = 12`).Error)
	plus, err := service.AllocateLocalCode(
		context.Background(), "GMAIL-PLUS", 2, 11, 12, tradedomain.SupplyPolicyPublicOnly,
		tradeapp.GmailSupplyQuote{Source: SourceLocal, CostPoints: "0"},
	)
	require.NoError(t, err)
	plusAllocation := mustLocalGmailAllocation(t, db, plus.AllocationID)
	require.Equal(t, GmailMailboxPlus, plusAllocation.Mailbox)
	require.Contains(t, plusAllocation.Email, "+p")
}

func TestGmailMailboxPreferencesHonorProductWeights(t *testing.T) {
	for _, test := range []struct {
		name   string
		config localGmailProductConfig
		want   string
	}{
		{name: "main", config: localGmailProductConfig{ProductID: 1, MainWeight: 1}, want: GmailMailboxMain},
		{name: "dot", config: localGmailProductConfig{ProductID: 1, DotWeight: 1}, want: GmailMailboxDot},
		{name: "plus", config: localGmailProductConfig{ProductID: 1, PlusWeight: 1}, want: GmailMailboxPlus},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, []string{test.want}, gmailMailboxPreferences("ORDER", test.config))
		})
	}
}

func mustLocalGmailAllocation(t *testing.T, db *gorm.DB, allocationID uint) allocationModel {
	t.Helper()
	var allocation allocationModel
	require.NoError(t, db.First(&allocation, allocationID).Error)
	return allocation
}

func prepareLocalGmailAllocationTestSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT NOT NULL, role TEXT NOT NULL)").Error)
	require.NoError(t, db.Exec("CREATE TABLE projects (id INTEGER PRIMARY KEY, status TEXT NOT NULL, access_type TEXT NOT NULL)").Error)
	require.NoError(t, db.Exec("CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL, code_enabled INTEGER NOT NULL, purchase_enabled INTEGER NOT NULL, main_weight INTEGER NOT NULL DEFAULT 1, dot_weight INTEGER NOT NULL DEFAULT 0, plus_weight INTEGER NOT NULL DEFAULT 0)").Error)
	require.NoError(t, db.Exec("CREATE TABLE project_accesses (project_id INTEGER NOT NULL, user_id INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO users(id, status, role) VALUES (1, 'active', 'supplier')").Error)
	require.NoError(t, db.Exec("INSERT INTO projects(id, status, access_type) VALUES (11, 'listed', 'public')").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type, status, code_enabled, purchase_enabled, main_weight, dot_weight, plus_weight) VALUES (12, 11, 'gmail', 'enabled', 1, 1, 1, 0, 0)").Error)
}
