package gmail

import (
	"context"
	"strings"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailPurchaseUsesUnifiedAllocationAndOrderLookup(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-local-purchase")
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "purchase@gmail.com", Identity: "purchase@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "abcdefghijklmnop", ForSale: true, Status: localResourceRollbackNormal,
	}).Error)

	allocator := allocapp.NewUseCase(allocinfra.NewRepo(db))
	allocation := allocateLocalGmailTest(t, allocator, "PURCHASE-1", 2, 12, allocdomain.GmailServiceModePurchase, allocdomain.SupplyScopePublic)
	service := NewService(db, nil)
	delivery, err := service.FindLocalPurchase(context.Background(), "PURCHASE-1")
	require.NoError(t, err)
	require.Equal(t, allocation.ID, delivery.AllocationID)
	require.Equal(t, allocation.Email, delivery.Email)
	require.NotEqual(t, "purchase@gmail.com", delivery.Email)
	require.Equal(t, "password", delivery.Password)

	var resource localResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, localResourceRollbackNormal, resource.Status, "allocation occupancy must not overwrite resource health")
	stored := mustLocalGmailAllocation(t, db, allocation.ID)
	require.EqualValues(t, 11, stored.ProjectID)
	require.EqualValues(t, 12, stored.ProductID)
	require.Equal(t, GmailMailboxDot, stored.Mailbox)
	require.Equal(t, AllocationStatusAllocated, stored.Status)

	replayed := allocateLocalGmailTest(t, allocator, "PURCHASE-1", 2, 12, allocdomain.GmailServiceModePurchase, allocdomain.SupplyScopePublic)
	require.Equal(t, allocation.ID, replayed.ID)
	dot, err := allocator.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "PURCHASE-2", BuyerUserID: 2, ProjectProductID: 12,
		ServiceMode: allocdomain.GmailServiceModePurchase, SupplyScope: allocdomain.SupplyScopePublic,
	})
	require.NoError(t, err)
	require.NotNil(t, dot)
	require.Equal(t, GmailMailboxDot, mustLocalGmailAllocation(t, db, dot.ID).Mailbox)

	_, err = allocator.ReleaseByOrder(context.Background(), "PURCHASE-1")
	require.NoError(t, err)
	require.Equal(t, AllocationStatusReleased, mustLocalGmailAllocation(t, db, allocation.ID).Status)
}

func TestUnifiedGmailAllocationReusesResourceWithUnusedProjectAlias(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-local-purchase-history")
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
	historyEmail := "h.istory@gmail.com"
	releasedAt := time.Now().UTC()
	require.NoError(t, db.Create(&allocationModel{
		OrderNo: "HISTORY-1", GuardType: "gmail", ProjectID: 11, ProductID: 12,
		Source: SourceLocal, ServiceMode: string(allocdomain.GmailServiceModeCode), ResourceID: &historyResourceID,
		SupplyScope: AllocationSupplyPublic, Mailbox: GmailMailboxDot, Email: historyEmail, Status: AllocationStatusReleased,
		ReleasedAt: &releasedAt,
	}).Error)

	allocation := allocateLocalGmailTest(
		t, allocapp.NewUseCase(allocinfra.NewRepo(db)), "PURCHASE-AFTER-HISTORY", 2, 12,
		allocdomain.GmailServiceModePurchase, allocdomain.SupplyScopePublic,
	)
	require.Equal(t, resources[0].ID, allocation.ResourceID)
	require.Equal(t, GmailMailboxDot, allocation.Mailbox)
	require.NotEqual(t, historyEmail, allocation.Email)
}

func TestLocalGmailSupplyIgnoresUpstreamHistoryButKeepsLocalHistory(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-local-supply-history")
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1, Email: "supply@gmail.com", Identity: "supply@gmail.com",
		Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
		ForSale: true, Status: LocalResourceNormal,
	}).Error)
	resourceID := root.ID
	require.NoError(t, db.Create(&allocationModel{
		OrderNo: "UPSTREAM-HISTORY", GuardType: "gmail", ProjectID: 11, ProductID: 12,
		Source: "smsbower", ServiceMode: string(allocdomain.GmailServiceModeCode), ResourceID: &resourceID,
		SupplyScope: AllocationSupplyPublic, Mailbox: GmailMailboxMain, Email: "supply@gmail.com",
		Status: AllocationStatusAllocated,
	}).Error)

	service := NewService(db, nil)
	quote, err := service.CheckSupply(
		context.Background(), 11, 12, 2,
		tradedomain.ServiceModeCode, tradedomain.SupplyPolicyPublicOnly, "1.00",
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, quote.Available)

	// An active main allocation in another project must not consume this
	// project's dot-alias supply.
	require.NoError(t, db.Model(&allocationModel{}).Where("order_no = ?", "UPSTREAM-HISTORY").Updates(map[string]any{
		"source": SourceLocal, "project_id": 12, "status": AllocationStatusAllocated,
	}).Error)
	quote, err = service.CheckSupply(
		context.Background(), 11, 12, 2,
		tradedomain.ServiceModeCode, tradedomain.SupplyPolicyPublicOnly, "1.00",
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, quote.Available)

	require.NoError(t, db.Model(&allocationModel{}).Where("order_no = ?", "UPSTREAM-HISTORY").Updates(map[string]any{
		"project_id": 11, "status": AllocationStatusReleased,
	}).Error)
	quote, err = service.CheckSupply(
		context.Background(), 11, 12, 2,
		tradedomain.ServiceModeCode, tradedomain.SupplyPolicyPublicOnly, "1.00",
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, quote.Available, "main history must preserve dot-alias supply")
}

func TestLocalGmailSupplyExcludesDotExhaustedResources(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-local-dot-supply-exhausted")
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "ab@gmail.com", Identity: "ab@gmail.com", AppPassword: "abcdefghijklmnop",
		ForSale: true, Status: LocalResourceNormal,
	}).Error)
	resourceID := root.ID
	require.NoError(t, db.Create(&allocationModel{
		OrderNo: "DOT-EXHAUSTED", GuardType: "gmail", ProjectID: 11, ProductID: 12,
		Source: SourceLocal, ServiceMode: string(allocdomain.GmailServiceModeCode), ResourceID: &resourceID,
		SupplyScope: AllocationSupplyPublic, Mailbox: GmailMailboxDot, Email: "a.b@gmail.com",
		Status: AllocationStatusReleased,
	}).Error)

	_, err := NewService(db, nil).CheckSupply(
		context.Background(), 11, 12, 2,
		tradedomain.ServiceModeCode, tradedomain.SupplyPolicyPublicOnly, "1.00",
	)
	require.ErrorIs(t, err, tradedomain.ErrUpstreamUnavailable)
}

func TestUnifiedGmailAllocationHonorsPrivateFirstAndPublicOnly(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-local-allocation-policy")
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

	allocator := allocapp.NewUseCase(allocinfra.NewRepo(db))
	owned, err := allocator.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "GMAIL-PRIVATE-FIRST", BuyerUserID: 2, ProjectProductID: 12,
		ServiceMode:  allocdomain.GmailServiceModeCode,
		SupplyScopes: []allocdomain.SupplyScope{allocdomain.SupplyScopeOwned, allocdomain.SupplyScopePublic},
	})
	require.NoError(t, err)
	require.Equal(t, privateRoot.ID, owned.ResourceID)
	require.Equal(t, allocdomain.SupplyScopeOwned, owned.SupplyScope)
	require.Equal(t, "0.00", mustLocalGmailAllocation(t, db, owned.ID).CostPointsSnapshot)

	public := allocateLocalGmailTest(
		t, allocator, "GMAIL-PUBLIC-ONLY", 2, 12,
		allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic,
	)
	require.Equal(t, publicRoot.ID, public.ResourceID)
	require.Equal(t, allocdomain.SupplyScopePublic, public.SupplyScope)
	require.Equal(t, "7.00", mustLocalGmailAllocation(t, db, public.ID).CostPointsSnapshot)
}

func TestUnifiedGmailProductsUseFixedMailboxKinds(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-fixed-product-mailboxes")
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_supplier_price, purchase_supplier_price, main_weight, dot_weight, plus_weight
) VALUES (13, 11, 'gmail_variant', 'enabled', 1, 1, '9', '10', 1, 1, 0)`).Error)
	require.NoError(t, db.Exec("UPDATE project_products SET main_weight = 0, dot_weight = 0, plus_weight = 1 WHERE id = 12").Error)
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "fixed@gmail.com", Identity: "fixed@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", ForSale: true, Status: LocalResourceNormal,
	}).Error)

	allocator := allocapp.NewUseCase(allocinfra.NewRepo(db))
	main := allocateLocalGmailTest(t, allocator, "GMAIL-FIXED-MAIN", 2, 12, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	mainAllocation := mustLocalGmailAllocation(t, db, main.ID)
	require.Equal(t, GmailMailboxDot, mainAllocation.Mailbox)
	require.NotEqual(t, "fixed@gmail.com", mainAllocation.Email)
	variant := allocateLocalGmailTest(t, allocator, "GMAIL-FIXED-VARIANT", 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.Equal(t, GmailMailboxPlus, mustLocalGmailAllocation(t, db, variant.ID).Mailbox)

	quote, err := NewService(db, nil).CheckSupply(
		context.Background(), 11, 13, 2, tradedomain.ServiceModeCode, tradedomain.SupplyPolicyPublicOnly, "1.00",
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, quote.Available)
}

func TestUnifiedGmailAllocationRechecksProductModeAndPublicOwner(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-local-allocation-recheck")
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1, Email: "guarded@gmail.com", Identity: "guarded@gmail.com",
		Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", ForSale: true, Status: LocalResourceNormal,
	}).Error)
	allocator := allocapp.NewUseCase(allocinfra.NewRepo(db))

	require.NoError(t, db.Exec("UPDATE project_products SET purchase_enabled = 0 WHERE id = 12").Error)
	_, err := allocator.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "GMAIL-MODE-DISABLED", BuyerUserID: 2, ProjectProductID: 12,
		ServiceMode: allocdomain.GmailServiceModePurchase, SupplyScope: allocdomain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, allocdomain.ErrProjectNotAllocatable)

	require.NoError(t, db.Exec("UPDATE users SET role = 'user' WHERE id = 1").Error)
	_, err = allocator.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "GMAIL-INELIGIBLE-OWNER", BuyerUserID: 2, ProjectProductID: 12,
		ServiceMode: allocdomain.GmailServiceModeCode, SupplyScope: allocdomain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, allocdomain.ErrInsufficientInventory)
}

func TestUnifiedGmailDotAndPlusHistoryIsProjectScoped(t *testing.T) {
	db := newLocalGmailAllocationTestDB(t, "gmail-local-alias-allocation")
	require.NoError(t, db.Exec("INSERT INTO projects(id, status, access_type) VALUES (21, 'listed', 'public')").Error)
	require.NoError(t, db.Exec(`
INSERT INTO project_products(
    id, project_id, type, status, code_enabled, purchase_enabled,
    code_supplier_price, purchase_supplier_price, main_weight, dot_weight, plus_weight
) VALUES (22, 21, 'gmail', 'enabled', 1, 1, '0', '0', 0, 1, 0),
         (13, 11, 'gmail_variant', 'enabled', 1, 1, '0', '0', 1, 1, 0)`).Error)

	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "firstname@gmail.com", Identity: "firstname@gmail.com", Password: "password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password", ForSale: true, Status: LocalResourceNormal,
	}).Error)
	allocator := allocapp.NewUseCase(allocinfra.NewRepo(db))
	createdAt := time.Now().UTC().Add(-time.Hour)
	for _, history := range []allocapp.HistoricalGmailAllocationCommand{
		{ProjectID: 11, ProductID: 12, ResourceID: root.ID, Mailbox: allocdomain.GmailMailboxMain, Email: "firstname@gmail.com", CreatedAt: createdAt, ReleasedAt: createdAt},
		{ProjectID: 21, ProductID: 22, ResourceID: root.ID, Mailbox: allocdomain.GmailMailboxMain, Email: "firstname@gmail.com", CreatedAt: createdAt, ReleasedAt: createdAt},
	} {
		_, err := allocator.ImportHistoricalGmailAllocation(context.Background(), history)
		require.NoError(t, err)
	}
	allocateDot := func(orderNo string, productID uint) allocationModel {
		result := allocateLocalGmailTest(
			t, allocator, orderNo, 2, productID,
			allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic,
		)
		allocation := mustLocalGmailAllocation(t, db, result.ID)
		require.Equal(t, GmailMailboxDot, allocation.Mailbox)
		require.NotEqual(t, "firstname@gmail.com", allocation.Email)
		return allocation
	}

	first := allocateDot("GMAIL-DOT-FIRST", 12)
	_, err := allocator.ReleaseByOrder(context.Background(), first.OrderNo)
	require.NoError(t, err)
	second := allocateDot("GMAIL-DOT-SECOND", 12)
	require.NotEqual(t, first.Email, second.Email, "one project must not reuse the same dot alias")
	_, err = allocator.ReleaseByOrder(context.Background(), second.OrderNo)
	require.NoError(t, err)
	otherProject := allocateDot("GMAIL-DOT-OTHER-PROJECT", 22)
	require.Equal(t, first.Email, otherProject.Email, "another project may reuse the same Gmail dot alias")

	_, err = allocator.ReleaseByOrder(context.Background(), otherProject.OrderNo)
	require.NoError(t, err)
	plus := allocateLocalGmailTest(
		t, allocator, "GMAIL-PLUS", 2, 13,
		allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic,
	)
	plusAllocation := mustLocalGmailAllocation(t, db, plus.ID)
	require.Equal(t, GmailMailboxPlus, plusAllocation.Mailbox)
	parts := strings.SplitN(plusAllocation.Email, "@", 2)
	require.Len(t, parts, 2)
	plusIndex := strings.LastIndexByte(parts[0], '+')
	require.Greater(t, plusIndex, 0)
	suffix := parts[0][plusIndex+1:]
	require.GreaterOrEqual(t, len(suffix), 4)
	require.LessOrEqual(t, len(suffix), 12)
	hasLetter, hasDigit := false, false
	for _, character := range suffix {
		switch {
		case character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z':
			hasLetter = true
		case character >= '0' && character <= '9':
			hasDigit = true
		default:
			t.Fatalf("Gmail plus suffix contains non-alphanumeric character: %q", plusAllocation.Email)
		}
	}
	require.True(t, hasLetter)
	require.True(t, hasDigit)
}

func newLocalGmailAllocationTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &localAllocationGuardModel{}, &allocationModel{}))
	prepareLocalGmailAllocationTestSchema(t, db)
	return db
}

func allocateLocalGmailTest(
	t *testing.T,
	allocator *allocapp.UseCase,
	orderNo string,
	buyerUserID, productID uint,
	mode allocdomain.GmailServiceMode,
	scope allocdomain.SupplyScope,
) *allocdomain.UnifiedAllocation {
	t.Helper()
	result, err := allocator.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: orderNo, BuyerUserID: buyerUserID, ProjectProductID: productID,
		ServiceMode: mode, SupplyScope: scope,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, allocdomain.AllocationTypeGmail, result.Type)
	return result
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
	require.NoError(t, db.Exec("CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL, code_enabled INTEGER NOT NULL, purchase_enabled INTEGER NOT NULL, code_supplier_price TEXT NOT NULL DEFAULT '0', purchase_supplier_price TEXT NOT NULL DEFAULT '0', main_weight INTEGER NOT NULL DEFAULT 1, dot_weight INTEGER NOT NULL DEFAULT 0, plus_weight INTEGER NOT NULL DEFAULT 0)").Error)
	require.NoError(t, db.Exec("CREATE TABLE project_accesses (project_id INTEGER NOT NULL, user_id INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO users(id, status, role) VALUES (1, 'active', 'supplier')").Error)
	require.NoError(t, db.Exec("INSERT INTO projects(id, status, access_type) VALUES (11, 'listed', 'public')").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type, status, code_enabled, purchase_enabled, code_supplier_price, purchase_supplier_price, main_weight, dot_weight, plus_weight) VALUES (12, 11, 'gmail', 'enabled', 1, 1, '7', '8', 1, 0, 0)").Error)
}
