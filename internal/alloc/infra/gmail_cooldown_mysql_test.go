package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	miniredisserver "github.com/alicebob/miniredis/v2/server"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	gmailapp "github.com/donnel666/remail/internal/gmail"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGmailProjectCooldownInventoryMySQL(t *testing.T) {
	previous, existed := runtimeconfig.Snapshot()[runtimeconfig.GmailVariantCooldownMinutesKey]
	runtimeconfig.Set(runtimeconfig.GmailVariantCooldownMinutesKey, "5")
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(runtimeconfig.GmailVariantCooldownMinutesKey, previous)
		} else {
			runtimeconfig.Delete(runtimeconfig.GmailVariantCooldownMinutesKey)
		}
	})
	db := newAllocMySQLTestDB(t)
	seedAllocBase(t, db, "gmail", 1, 0, 0)
	require.NoError(t, db.Exec(`INSERT INTO projects(id, name, target_platform, status, access_type)
VALUES (11, 'Project B', 'alloc', 'listed', 'public')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_products(
id, project_id, type, status, code_enabled, purchase_enabled, main_weight, dot_weight, plus_weight
) VALUES (21, 10, 'gmail_variant', 'enabled', TRUE, FALSE, 0, 0, 1),
         (22, 11, 'gmail', 'enabled', TRUE, FALSE, 1, 0, 0),
         (23, 11, 'gmail_variant', 'enabled', TRUE, FALSE, 0, 0, 1)`).Error)
	seedGmailResources(t, db, []gmailResourceSeed{
		{ID: 1000, OwnerUserID: 1, Email: "ab@gmail.com", ForSale: true},
		{ID: 1001, OwnerUserID: 2, Email: "cd@gmail.com", ForSale: false},
	})
	server := miniredis.RunT(t)
	server.Server().SetPreHook(func(peer *miniredisserver.Peer, cmd string, _ ...string) bool {
		if cmd == "SCAN" || cmd == "KEYS" {
			peer.WriteError("allocation and inventory must not scan Redis")
			return true
		}
		return false
	})
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cooldown := gmailapp.NewService(db, nil)
	cooldown.SetResourceImportDependencies(client, nil)
	repo := NewRepo(db)
	repo.SetGmailVariantCooldownPort(cooldown)
	uc := allocapp.NewUseCase(repo)
	uc.SetGmailVariantCooldownPort(cooldown)
	ctx := context.Background()
	allocate := func(orderNo string, productID uint, scope domain.SupplyScope) *domain.UnifiedAllocation {
		t.Helper()
		allocation, err := uc.Allocate(ctx, allocapp.AllocateCommand{
			OrderNo: orderNo, BuyerUserID: 2, ProjectProductID: productID,
			ServiceMode: domain.GmailServiceModeCode, SupplyScope: scope,
		})
		require.NoError(t, err)
		return allocation
	}
	first := allocate("gmail-public-project-a", 21, domain.SupplyScopePublic)
	totals, err := repo.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, totals.Items[0].PublicAvailable, "primary Gmail stock remains usable")
	require.Zero(t, totals.Items[1].PublicAvailable, "Project A variant stock excludes its cooling source")
	other, err := repo.GetInventoryStats(ctx, 11)
	require.NoError(t, err)
	require.EqualValues(t, 3, other.Gmail.DotAvailable)
	require.Equal(t, allocapp.GmailVariantInventory, other.Gmail.PlusAvailable)
	private, err := repo.ListPrivateGmailInventoryTotals(ctx, 10, 2)
	require.NoError(t, err)
	require.Len(t, private, 2)
	allocate("gmail-private-project-a", 21, domain.SupplyScopeOwned)
	private, err = repo.ListPrivateGmailInventoryTotals(ctx, 10, 2)
	require.NoError(t, err)
	require.Len(t, private, 1)
	require.EqualValues(t, 20, private[0].ProductID)
	private, err = repo.ListPrivateGmailInventoryTotals(ctx, 11, 2)
	require.NoError(t, err)
	require.Len(t, private, 2, "Project B private variants are still available")
	second := allocate("gmail-public-project-b", 23, domain.SupplyScopePublic)
	require.Equal(t, first.ResourceID, second.ResourceID)
	server.FastForward(5 * time.Minute)
	stats, err := repo.GetInventoryStats(ctx, 10)
	require.NoError(t, err)
	expectedDot := int64(3)
	if first.Mailbox == string(domain.GmailMailboxDot) {
		expectedDot--
	}
	require.Equal(t, expectedDot, stats.Gmail.DotAvailable)
	require.Equal(t, allocapp.GmailVariantInventory, stats.Gmail.PlusAvailable)
	var globalCooldowns int64
	require.NoError(t, db.Table("gmail_resources").Where("status = ?", "cooldown").Count(&globalCooldowns).Error)
	require.Zero(t, globalCooldowns)

	allocate("gmail-public-new-cooldown", 21, domain.SupplyScopePublic)
	allocate("gmail-private-new-cooldown", 21, domain.SupplyScopeOwned)
	runtimeconfig.Set(runtimeconfig.GmailVariantCooldownMinutesKey, "0")
	server.SetError("Redis temporarily unavailable")
	stats, err = repo.GetInventoryStats(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, allocapp.GmailVariantInventory, stats.Gmail.PlusAvailable)
	private, err = repo.ListPrivateGmailInventoryTotals(ctx, 10, 2)
	require.NoError(t, err)
	require.Len(t, private, 2, "disabled cooldown must not block private variant stock")
	allocate("gmail-public-disabled-cooldown", 21, domain.SupplyScopePublic)
	allocate("gmail-private-disabled-cooldown", 21, domain.SupplyScopeOwned)
}
