package gmail

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGmailVariantCooldownUsesRedisTTLAndRestoresAllocation(t *testing.T) {
	setGmailRuntime(t, map[string]string{runtimeconfig.GmailVariantCooldownMinutesKey: "5"})
	db := newLocalGmailAllocationTestDB(t, "gmail-variant-cooldown")
	require.NoError(t, db.Exec(`INSERT INTO project_products(
		id, project_id, type, status, code_enabled, purchase_enabled,
		code_supplier_price, purchase_supplier_price, main_weight, dot_weight, plus_weight
	) VALUES (13, 11, 'gmail_variant', 'enabled', 1, 1, '0', '0', 0, 0, 1)`).Error)
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
	allocator := allocapp.NewUseCase(allocinfra.NewRepo(db))
	allocator.SetGmailVariantCooldownPort(service)

	allocateLocalGmailTest(t, allocator, "GMAIL-MAIN-NO-COOLDOWN", 2, 12, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	var resource localResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, LocalResourceNormal, resource.Status)
	require.False(t, server.Exists(localGmailVariantCooldownKey(root.ID)))

	allocateLocalGmailTest(t, allocator, "GMAIL-VARIANT-COOLDOWN", 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, LocalResourceCooldown, resource.Status)
	require.Equal(t, 5*time.Minute, server.TTL(localGmailVariantCooldownKey(root.ID)))

	items := []LocalResourceItem{{ID: root.ID, Status: LocalResourceCooldown}}
	require.NoError(t, service.enrichVariantCooldowns(context.Background(), items))
	require.NotNil(t, items[0].CooldownUntil)
	require.WithinDuration(t, time.Now().UTC().Add(5*time.Minute), *items[0].CooldownUntil, time.Second)

	_, err := allocator.Allocate(context.Background(), allocapp.AllocateCommand{
		OrderNo: "GMAIL-VARIANT-BLOCKED", BuyerUserID: 2, ProjectProductID: 13,
		ServiceMode: allocdomain.GmailServiceModeCode, SupplyScope: allocdomain.SupplyScopePublic,
	})
	require.ErrorIs(t, err, allocdomain.ErrInsufficientInventory)

	server.FastForward(5 * time.Minute)
	require.NoError(t, service.RestoreExpiredVariantCooldowns(context.Background()))
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, localResourceRollbackNormal, resource.Status)
	allocateLocalGmailTest(t, allocator, "GMAIL-VARIANT-RESTORED", 2, 13, allocdomain.GmailServiceModeCode, allocdomain.SupplyScopePublic)
}

func TestGmailVariantCooldownCanBeDisabled(t *testing.T) {
	setGmailRuntime(t, map[string]string{runtimeconfig.GmailVariantCooldownMinutesKey: "0"})
	db := newLocalGmailAllocationTestDB(t, "gmail-variant-cooldown-disabled")
	root := resourceRootModel{Type: "gmail", OwnerUserID: 1}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 1,
		Email: "disabled@gmail.com", Identity: "disabled@gmail.com", AppPassword: "abcdefghijklmnop",
		ForSale: true, Status: LocalResourceNormal,
	}).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	service := NewService(db, nil)
	service.SetResourceImportDependencies(client, nil)

	require.NoError(t, service.StartVariantCooldown(context.Background(), root.ID))
	var resource localResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, LocalResourceNormal, resource.Status)
	require.False(t, server.Exists(localGmailVariantCooldownKey(root.ID)))
}
