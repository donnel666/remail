package infra

import (
	"context"
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newDaumInventoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	for _, sql := range []string{
		`CREATE TABLE users(id INTEGER PRIMARY KEY,status TEXT,role TEXT)`,
		`CREATE TABLE email_resources(id INTEGER PRIMARY KEY,type TEXT,owner_user_id INTEGER)`,
		`CREATE TABLE daum_resources(id INTEGER PRIMARY KEY,resource_type TEXT,owner_user_id INTEGER,email_address TEXT,email_domain TEXT,status TEXT,for_sale BOOLEAN,alloc_bucket INTEGER,quality_score INTEGER,last_allocated_at DATETIME,app_password TEXT)`,
		`CREATE TABLE daum_explicit_aliases(id INTEGER PRIMARY KEY,resource_id INTEGER,email_address TEXT,email_domain TEXT,status TEXT)`,
		`CREATE TABLE daum_allocations(id INTEGER PRIMARY KEY,resource_id INTEGER,project_id INTEGER,email TEXT,order_no TEXT,guard_type TEXT,status TEXT,mailbox TEXT,explicit_alias_id INTEGER)`,
		`CREATE TABLE project_daum_suffix_blacklists(project_id INTEGER,suffix TEXT)`,
		`INSERT INTO users VALUES(1,'active','supplier'),(2,'active','user')`,
		`INSERT INTO email_resources VALUES(10,'daum',1),(11,'daum',2)`,
		`INSERT INTO daum_resources VALUES(10,'daum',1,'public@daum.net','daum.net','normal',TRUE,10,100,NULL,'app-test'),(11,'daum',2,'private@daum.net','daum.net','normal',FALSE,11,100,NULL,'app-test')`,
		`INSERT INTO daum_explicit_aliases VALUES(101,10,'public@hanmail.net','hanmail.net','normal'),(102,10,'public@hanmail2.net','hanmail2.net','normal'),(111,11,'private@hanmail.net','hanmail.net','normal'),(112,11,'private@hanmail2.net','hanmail2.net','normal')`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	return db
}

func TestDaumInventoryPreservesAddressHistoryAndRootHealth(t *testing.T) {
	db := newDaumInventoryDB(t)
	repo, ctx := NewRepo(db), context.Background()
	assertInventory := func(project uint, want map[string]int64) {
		t.Helper()
		got, err := repo.daumSuffixInventory(ctx, project, 2, domain.SupplyScopePublic)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	full := map[string]int64{"daum.net": 1, "hanmail.net": 1, "hanmail2.net": 1}
	assertInventory(1, full)
	rows, err := repo.ListDaumSourceCandidates(ctx, 1, 2, domain.SupplyScopePublic, []uint16{10}, 10, "hanmail2.net")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "public@hanmail2.net", rows[0].Email)
	require.Equal(t, "alias", rows[0].Mailbox)
	require.EqualValues(t, 102, *rows[0].ExplicitAliasID)
	require.NoError(t, db.Exec(`INSERT INTO daum_allocations VALUES(1,10,1,'public@hanmail.net','USED','daum','released','alias',101)`).Error)
	assertInventory(1, map[string]int64{"daum.net": 1, "hanmail.net": 0, "hanmail2.net": 1})
	assertInventory(2, full)
	require.NoError(t, db.Exec(`INSERT INTO project_daum_suffix_blacklists VALUES(1,'hanmail2.net')`).Error)
	assertInventory(1, map[string]int64{"daum.net": 1, "hanmail.net": 0, "hanmail2.net": 0})
	locked, err := repo.LockDaumCandidate(ctx, rows[0], 1, 2, domain.SupplyScopePublic, "hanmail2.net")
	require.NoError(t, err)
	require.Nil(t, locked, "an address blocked since listing must fail the locked recheck")
	private, err := repo.daumSuffixInventory(ctx, 2, 2, domain.SupplyScopeOwned)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"daum.net": 1, "hanmail.net": 0, "hanmail2.net": 0}, private)
	require.NoError(t, db.Exec(`UPDATE daum_resources SET status='abnormal' WHERE id=10`).Error)
	assertInventory(2, map[string]int64{"daum.net": 0, "hanmail.net": 0, "hanmail2.net": 0})
	require.NoError(t, db.Exec(`UPDATE daum_resources SET status='normal', app_password='' WHERE id=10`).Error)
	assertInventory(2, map[string]int64{"daum.net": 0, "hanmail.net": 0, "hanmail2.net": 0})
	rows, err = repo.ListDaumSourceCandidates(ctx, 2, 2, domain.SupplyScopePublic, []uint16{10}, 10, "hanmail2.net")
	require.NoError(t, err)
	require.Empty(t, rows, "normal state without credentials cannot be sold")
}

func TestDaumOwnedInventoryAndAllocationDoNotExposePlatformAliases(t *testing.T) {
	db := newDaumInventoryDB(t)
	repo, ctx := NewRepo(db), context.Background()
	require.NoError(t, db.Exec(`CREATE TABLE projects(id INTEGER PRIMARY KEY,status TEXT);
CREATE TABLE project_products(id INTEGER PRIMARY KEY,project_id INTEGER,type TEXT,status TEXT);
INSERT INTO projects VALUES(1,'listed');
INSERT INTO project_products VALUES(20,1,'daum','enabled')`).Error)

	private, err := repo.ListPrivateDaumInventoryTotals(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, []allocapp.PrivateProductInventoryTotal{{ProductID: 20, Suffix: "daum.net", Available: 1}}, private)
	other, err := repo.ListPrivateDaumInventoryTotals(ctx, 1, 1)
	require.NoError(t, err)
	require.Empty(t, other)

	for _, suffix := range []string{"", "daum.net", "hanmail.net", "hanmail2.net"} {
		t.Run(suffix, func(t *testing.T) {
			owned, err := repo.ListDaumSourceCandidates(ctx, 1, 2, domain.SupplyScopeOwned, []uint16{10, 11}, 10, suffix)
			require.NoError(t, err)
			if suffix == "" || suffix == "daum.net" {
				require.Len(t, owned, 1)
				require.Equal(t, "private@daum.net", owned[0].Email)
				locked, err := repo.LockDaumCandidate(ctx, owned[0], 1, 2, domain.SupplyScopeOwned, suffix)
				require.NoError(t, err)
				require.NotNil(t, locked)
				return
			}
			require.Empty(t, owned)
			locked, err := repo.LockDaumCandidate(ctx, allocapp.DaumCandidate{ResourceID: 11, Email: "private@" + suffix}, 1, 2, domain.SupplyScopeOwned, suffix)
			require.NoError(t, err)
			require.Nil(t, locked, "an alias candidate from an older reader must fail the owned lock recheck")
		})
	}

	public, err := repo.ListDaumSourceCandidates(ctx, 1, 2, domain.SupplyScopePublic, []uint16{10, 11}, 10, "")
	require.NoError(t, err)
	require.Len(t, public, 3)
	for _, candidate := range public {
		require.EqualValues(t, 10, candidate.ResourceID)
		locked, err := repo.LockDaumCandidate(ctx, candidate, 1, 2, domain.SupplyScopePublic, "")
		require.NoError(t, err)
		require.NotNil(t, locked, "public purchases keep all three addresses")
	}
	// Already granted alias orders retain their exact address authorization.
	require.NoError(t, db.Exec(`INSERT INTO daum_allocations VALUES(1,11,1,'private@hanmail.net','OLD-OWNED','daum','allocated','alias',111)`).Error)
	ready, err := repo.DaumAllocationReady(ctx, "OLD-OWNED", 1)
	require.NoError(t, err)
	require.True(t, ready)
}

func TestDaumReadinessRejectsOtherAliasAndRootCredentialFailure(t *testing.T) {
	db := newDaumInventoryDB(t)
	repo, ctx := NewRepo(db), context.Background()
	require.NoError(t, db.Exec(`INSERT INTO daum_allocations VALUES(1,10,1,'public@hanmail2.net','ACTIVE','daum','allocated','alias',102)`).Error)
	ready, err := repo.DaumAllocationReady(ctx, "ACTIVE", 1)
	require.NoError(t, err)
	require.True(t, ready)
	require.NoError(t, db.Exec(`UPDATE daum_allocations SET explicit_alias_id=101`).Error)
	ready, err = repo.DaumAllocationReady(ctx, "ACTIVE", 1)
	require.NoError(t, err)
	require.False(t, ready)
	require.NoError(t, db.Exec(`UPDATE daum_allocations SET explicit_alias_id=102`).Error)
	require.NoError(t, db.Exec(`UPDATE daum_resources SET status='pending' WHERE id=10`).Error)
	ready, err = repo.DaumAllocationReady(ctx, "ACTIVE", 1)
	require.NoError(t, err)
	require.False(t, ready)
	require.NoError(t, db.Exec(`UPDATE daum_resources SET status='normal', app_password='' WHERE id=10`).Error)
	ready, err = repo.DaumAllocationReady(ctx, "ACTIVE", 1)
	require.NoError(t, err)
	require.False(t, ready, "normal state without credentials cannot fulfill a paid order")
	ready, err = repo.DaumAllocationReady(ctx, "NEW", 0)
	require.NoError(t, err)
	require.True(t, ready)
	ready, err = repo.DaumAllocationReady(ctx, "NEW", 1)
	require.NoError(t, err)
	require.False(t, ready)
	_, err = repo.LockDaumCandidate(ctx, allocapp.DaumCandidate{ResourceID: 10, Email: "public@hanmail2.net"}, 1, 2, domain.SupplyScopePublic, "other.example")
	require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
}

func TestDaumCandidatesRequireBoundedBuckets(t *testing.T) {
	for _, buckets := range [][]uint16{nil, {}, make([]uint16, 101), {allocapp.DaumBucketCount}, {2047}} {
		_, err := NewRepo(nil).ListDaumSourceCandidates(context.Background(), 10, 2, domain.SupplyScopePublic, buckets, 4, "daum.net")
		require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
	}
}
