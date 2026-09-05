package gmail

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	miniredisserver "github.com/alicebob/miniredis/v2/server"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGmailVariantCooldownIsProjectScoped(t *testing.T) {
	setGmailRuntime(t, map[string]string{runtimeconfig.GmailVariantCooldownMinutesKey: "5"})
	db := newLocalGmailAllocationTestDB(t, "gmail-project-cooldown")
	require.NoError(t, db.AutoMigrate(&gmailAdminTestUser{}, &gmailAdminTestGroup{}))
	require.NoError(t, db.Exec("ALTER TABLE projects ADD COLUMN name TEXT NOT NULL DEFAULT ''").Error)
	require.NoError(t, db.Exec("UPDATE projects SET name = 'Project A' WHERE id = 11").Error)
	require.NoError(t, db.Exec("INSERT INTO projects(id, name, status, access_type) VALUES (21, 'Project B', 'listed', 'public')").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type, status, code_enabled, purchase_enabled, main_weight, dot_weight, plus_weight) VALUES (13, 11, 'gmail_variant', 'enabled', 1, 1, 0, 0, 1), (22, 21, 'gmail', 'enabled', 1, 1, 1, 0, 0), (23, 21, 'gmail_variant', 'enabled', 1, 1, 0, 0, 1)").Error)
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "cooldown@gmail.com", Identity: "cooldown@gmail.com", AppPassword: "abcdefghijklmnop",
		ForSale: true, Status: LocalResourceNormal,
	}).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	service := NewService(db, nil)
	service.SetResourceImportDependencies(client, nil)
	clock := time.Now().UTC()
	service.now = func() time.Time { return clock }
	repo := allocinfra.NewRepo(db)
	repo.SetGmailVariantCooldownPort(service)
	allocator := allocapp.NewUseCase(repo)
	allocator.SetGmailVariantCooldownPort(service)
	ctx := context.Background()
	keyA, keyB := localGmailVariantCooldownKey(root.ID, 11), localGmailVariantCooldownKey(root.ID, 21)

	allocateLocalGmailTest(t, allocator, "GMAIL-MAIN-NO-COOLDOWN", 2, 12, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.False(t, server.Exists(keyA))
	first := allocateLocalGmailTest(t, allocator, "GMAIL-PROJECT-A", 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.Equal(t, 5*time.Minute, server.TTL(keyA))
	var resource localResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, LocalResourceNormal, resource.Status, "project cooldown must not overwrite resource health")

	server.FastForward(time.Minute)
	clock = clock.Add(time.Minute)
	replay := allocateLocalGmailTest(t, allocator, first.OrderNo, 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.Equal(t, first.ID, replay.ID)
	require.Equal(t, 4*time.Minute, server.TTL(keyA), "idempotent replay must not extend the cooldown")
	_, err := allocator.Allocate(ctx, allocapp.AllocateCommand{
		OrderNo: "GMAIL-A-OTHER-BUYER", BuyerUserID: 3, ProjectProductID: 13,
		ServiceMode: allocdomain.GmailServiceModePurchase, SupplyScope: allocdomain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, allocdomain.ErrInsufficientInventory)

	mainB := allocateLocalGmailTest(t, allocator, "GMAIL-B-MAIN", 2, 22, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.Equal(t, root.ID, mainB.ResourceID)
	require.False(t, server.Exists(keyB))
	second := allocateLocalGmailTest(t, allocator, "GMAIL-PROJECT-B", 2, 23, allocdomain.GmailServiceModePurchase, allocdomain.SupplyScopePublic)
	require.Equal(t, first.ResourceID, second.ResourceID, "Project B can use the same resource during Project A cooldown")
	require.Equal(t, 4*time.Minute, server.TTL(keyA))
	require.Equal(t, 5*time.Minute, server.TTL(keyB))

	page, err := service.ListLocalResources(ctx, LocalResourceListFilter{Status: LocalResourceCooldown})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.EqualValues(t, 1, page.Facets.Cooldown)
	require.Zero(t, page.Facets.Normal)
	require.Len(t, page.Items, 1)
	require.Equal(t, LocalResourceCooldown, page.Items[0].Status)
	require.Len(t, page.Items[0].ProjectCooldowns, 2)
	require.Equal(t, "Project A", page.Items[0].ProjectCooldowns[0].ProjectName)
	require.Equal(t, "Project B", page.Items[0].ProjectCooldowns[1].ProjectName)
	require.Equal(t, clock.Add(4*time.Minute), *page.Items[0].ProjectCooldowns[0].CooldownUntil)
	require.Equal(t, clock.Add(5*time.Minute), *page.Items[0].CooldownUntil)
	page, err = service.ListLocalResources(ctx, LocalResourceListFilter{Status: LocalResourceNormal})
	require.NoError(t, err)
	require.Zero(t, page.Total)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		ids, selectErr := service.resolveAdminLocalResourceSelectionTx(ctx, tx, AdminLocalResourceSelection{
			Mode: "filter", Filter: &LocalResourceListFilter{Status: LocalResourceCooldown},
		})
		require.NoError(t, selectErr)
		require.Equal(t, []uint{root.ID}, ids)
		return nil
	}))

	server.FastForward(4 * time.Minute)
	clock = clock.Add(4 * time.Minute)
	require.False(t, server.Exists(keyA))
	cooling, err := service.CoolingResourceIDs(ctx, 11, []uint{root.ID})
	require.NoError(t, err)
	require.Empty(t, cooling, "expiry restores allocation without a database dispatcher")
	_, err = allocator.Allocate(ctx, allocapp.AllocateCommand{
		OrderNo: "GMAIL-B-STILL-COOLING", BuyerUserID: 2, ProjectProductID: 23,
		ServiceMode: allocdomain.GmailServiceModeCode, SupplyScope: allocdomain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, allocdomain.ErrInsufficientInventory, "Project A expiry must not clear Project B cooldown")
	detail, err := service.GetAdminLocalResource(ctx, root.ID)
	require.NoError(t, err)
	require.Len(t, detail.ProjectCooldowns, 1)
	require.EqualValues(t, 21, detail.ProjectCooldowns[0].ProjectID)
	allocateLocalGmailTest(t, allocator, "GMAIL-A-RESTORED", 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, LocalResourceNormal, resource.Status)

	server.FastForward(5 * time.Minute)
	page, err = service.ListLocalResources(ctx, LocalResourceListFilter{Status: LocalResourceCooldown})
	require.NoError(t, err)
	require.Zero(t, page.Total)
	require.Zero(t, page.Facets.Cooldown)
	require.EqualValues(t, 1, page.Facets.Normal)
}

func TestGmailVariantCooldownCanBeDisabled(t *testing.T) {
	setGmailRuntime(t, map[string]string{runtimeconfig.GmailVariantCooldownMinutesKey: "0"})
	service := NewService(nil, nil)
	started, err := service.StartVariantCooldown(context.Background(), 1, 11)
	require.NoError(t, err)
	require.True(t, started)
	cooling, err := service.CoolingResourceIDs(context.Background(), 11, []uint{1})
	require.NoError(t, err)
	require.Empty(t, cooling)
	adminCooldowns, err := service.variantCooldowns(context.Background())
	require.NoError(t, err)
	require.Empty(t, adminCooldowns)
}

func TestGmailVariantCooldownReadsOnlyCandidates(t *testing.T) {
	setGmailRuntime(t, map[string]string{runtimeconfig.GmailVariantCooldownMinutesKey: "5"})
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	service := NewService(nil, nil)
	service.SetResourceImportDependencies(client, nil)
	ctx := context.Background()
	for _, resourceID := range []uint{1, 3} {
		require.NoError(t, client.Set(ctx, localGmailVariantCooldownKey(resourceID, 11), "token", 5*time.Minute).Err())
	}
	require.NoError(t, client.Set(ctx, localGmailVariantCooldownKey(2, 21), "other-project", 5*time.Minute).Err())
	before := server.CommandCount()
	cooling, err := service.CoolingResourceIDs(ctx, 11, []uint{2, 1, 1})
	require.NoError(t, err)
	require.Equal(t, []uint{1}, cooling, "other projects and non-candidate resources are not returned")
	require.Equal(t, 2, server.CommandCount()-before, "read only the two distinct candidate keys")
	server.SetError("Redis temporarily unavailable")
	cooling, err = service.CoolingResourceIDs(ctx, 11, nil)
	require.NoError(t, err)
	require.Empty(t, cooling, "an empty candidate page must not read Redis")
}

func TestGmailVariantCooldownSkipsCoolingPages(t *testing.T) {
	setGmailRuntime(t, map[string]string{
		runtimeconfig.GmailVariantCooldownMinutesKey: "5",
		"global_candidate_window":                    "2",
		"candidate_retry_count":                      "1",
	})
	db := newLocalGmailAllocationTestDB(t, "gmail-cooldown-pages")
	require.NoError(t, db.Exec("INSERT INTO project_products(id, project_id, type, status, code_enabled, purchase_enabled) VALUES (13, 11, 'gmail_variant', 'enabled', 1, 1)").Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	server.Server().SetPreHook(func(peer *miniredisserver.Peer, cmd string, _ ...string) bool {
		if cmd == "SCAN" || cmd == "KEYS" {
			peer.WriteError("allocation must not scan Redis")
			return true
		}
		return false
	})
	service := NewService(db, nil)
	service.SetResourceImportDependencies(client, nil)
	repo := allocinfra.NewRepo(db)
	repo.SetGmailVariantCooldownPort(service)
	allocator := allocapp.NewUseCase(repo)
	allocator.SetGmailVariantCooldownPort(service)
	ctx := context.Background()
	var resourceIDs []uint
	for i := 0; i < 5; i++ {
		root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
		require.NoError(t, db.Create(&root).Error)
		email := fmt.Sprintf("coolpage%d@gmail.com", i)
		require.NoError(t, db.Create(&localResourceModel{
			ID: root.ID, ResourceType: "gmail", OwnerUserID: 1, Email: email, Identity: email,
			AppPassword: "abcdefghijklmnop", ForSale: true, Status: LocalResourceNormal,
		}).Error)
		resourceIDs = append(resourceIDs, root.ID)
		if i < 4 {
			require.NoError(t, client.Set(ctx, localGmailVariantCooldownKey(root.ID, 11), "cooling", 5*time.Minute).Err())
		}
	}
	allocation := allocateLocalGmailTest(t, allocator, "GMAIL-AFTER-COOLING-PAGES", 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.Equal(t, resourceIDs[4], allocation.ResourceID, "skip two full cooling pages and reach the third page")
	_, err := allocator.Allocate(ctx, allocapp.AllocateCommand{
		OrderNo: "GMAIL-ALL-PAGES-COOLING", BuyerUserID: 2, ProjectProductID: 13,
		ServiceMode: allocdomain.GmailServiceModePurchase, SupplyScope: allocdomain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, allocdomain.ErrInsufficientInventory)
	server.FastForward(5 * time.Minute)
	allocation = allocateLocalGmailTest(t, allocator, "GMAIL-PAGES-EXPIRED", 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.Equal(t, resourceIDs[0], allocation.ResourceID)

	// Disabling the setting must bypass both Redis reads and writes, even when
	// every candidate still has a TTL and Redis is temporarily unavailable.
	for _, resourceID := range resourceIDs {
		require.NoError(t, client.Set(ctx, localGmailVariantCooldownKey(resourceID, 11), "cooling", 5*time.Minute).Err())
	}
	runtimeconfig.Set(runtimeconfig.GmailVariantCooldownMinutesKey, "0")
	server.SetError("Redis temporarily unavailable")
	for _, mode := range []allocdomain.GmailServiceMode{allocdomain.GmailServiceModeCode, allocdomain.GmailServiceModePurchase} {
		allocateLocalGmailTest(t, allocator, "GMAIL-DISABLED-COOLDOWN-"+string(mode), 2, 13, mode, allocdomain.SupplyScopePublic)
	}
	cooldowns, err := service.variantCooldowns(ctx)
	require.NoError(t, err)
	require.Empty(t, cooldowns)
}

func TestGmailVariantCooldownClaimAndRollback(t *testing.T) {
	setGmailRuntime(t, map[string]string{runtimeconfig.GmailVariantCooldownMinutesKey: "5"})
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	db := newLocalGmailAllocationTestDB(t, "gmail-cooldown-rollback")
	repo := allocinfra.NewRepo(db)
	service := NewService(db, nil)
	service.SetResourceImportDependencies(client, nil)
	ctx := context.Background()
	key := localGmailVariantCooldownKey(1, 11)
	rollbackErr := errors.New("later order write failed")
	err := repo.WithTx(ctx, func(txCtx context.Context) error {
		started, err := service.StartVariantCooldown(txCtx, 1, 11)
		require.NoError(t, err)
		require.True(t, started)
		started, err = service.StartVariantCooldown(txCtx, 1, 11)
		require.NoError(t, err)
		require.False(t, started, "SET NX prevents a concurrent same-project allocation")
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)
	require.False(t, server.Exists(key), "failed order must release its project cooldown")

	oldCtx, rollbackOld := platform.WithGormRollback(ctx)
	started, err := service.StartVariantCooldown(oldCtx, 1, 11)
	require.NoError(t, err)
	require.True(t, started)
	server.FastForward(5 * time.Minute)
	newCtx, _ := platform.WithGormRollback(ctx)
	started, err = service.StartVariantCooldown(newCtx, 1, 11)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, rollbackOld(ctx))
	require.True(t, server.Exists(key), "an old rollback must not delete a newer allocation's cooldown")

	server.SetError("Redis temporarily unavailable")
	_, err = service.CoolingResourceIDs(ctx, 11, []uint{1})
	require.Error(t, err)
	_, err = service.StartVariantCooldown(newCtx, 1, 21)
	require.Error(t, err)
}
