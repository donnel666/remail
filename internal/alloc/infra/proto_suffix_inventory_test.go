package infra

import (
	"context"
	"fmt"
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newProtoSuffixInventoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT, role TEXT)`,
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT, owner_user_id INTEGER)`,
		`CREATE TABLE proto_resources (id INTEGER PRIMARY KEY, resource_type TEXT, owner_user_id INTEGER, email_address TEXT, email_domain TEXT, status TEXT, for_sale BOOLEAN, credential_revision INTEGER DEFAULT 2, alloc_bucket INTEGER DEFAULT 1, quality_score INTEGER DEFAULT 100, last_allocated_at DATETIME)`,
		`CREATE TABLE proto_sessions (resource_id INTEGER PRIMARY KEY, credential_revision INTEGER)`,
		`CREATE TABLE proto_allocations (resource_id INTEGER, project_id INTEGER, guard_type TEXT DEFAULT 'proto', status TEXT DEFAULT 'released')`,
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, status TEXT)`,
		`CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER, type TEXT, status TEXT, code_enabled BOOLEAN, purchase_enabled BOOLEAN, main_weight INTEGER, dot_weight INTEGER, plus_weight INTEGER)`,
		`CREATE TABLE microsoft_allocations (project_id INTEGER, status TEXT)`,
		`CREATE TABLE domain_allocations (project_id INTEGER, status TEXT)`,
		`CREATE TABLE gmail_allocations (project_id INTEGER, status TEXT, source TEXT)`,
		`CREATE TABLE icloud_allocations (project_id INTEGER, status TEXT)`,
		`INSERT INTO users VALUES (1,'active','supplier'),(2,'active','user'),(3,'disabled','supplier'),(4,'active','user'),(5,'active','supplier')`,
		`INSERT INTO projects VALUES (10,'listed')`,
		`INSERT INTO project_products VALUES (20,10,'proto','enabled',TRUE,TRUE,1,0,0)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	for _, row := range []struct {
		id, owner uint
		suffix    string
		forSale   bool
	}{
		{1, 1, "proton.me", true}, {2, 1, "protonmail.com", true},
		{3, 2, "proton.me", false}, {4, 2, "protonmail.com", false},
		{5, 1, "proton.me", true}, {6, 1, "proton.me", true},
		{7, 1, "protonmail.com", true}, {8, 3, "proton.me", true},
		{9, 4, "protonmail.com", true}, {10, 1, "unsupported.test", true},
		{11, 5, "proton.me", false}, {12, 1, "protonmail.com", true},
		{13, 1, "protonmail.com", true}, {14, 1, "proton.me", true},
		{15, 2, "unsupported.test", false},
	} {
		require.NoError(t, db.Exec(`INSERT INTO email_resources VALUES (?, 'proto', ?)`, row.id, row.owner).Error)
		require.NoError(t, db.Exec(`INSERT INTO proto_resources(id,resource_type,owner_user_id,email_address,email_domain,status,for_sale) VALUES (?, 'proto', ?, ?, ?, 'normal', ?)`,
			row.id, row.owner, fmt.Sprintf("box%d@%s", row.id, row.suffix), row.suffix, row.forSale).Error)
		if row.id != 5 { // A normal resource still needs a current session.
			require.NoError(t, db.Exec(`INSERT INTO proto_sessions VALUES (?, 2)`, row.id).Error)
		}
	}
	for _, statement := range []string{
		`UPDATE proto_sessions SET credential_revision=1 WHERE resource_id=6`,
		`INSERT INTO proto_allocations(resource_id,project_id) VALUES (7,10),(14,11)`,
		`UPDATE email_resources SET owner_user_id=5 WHERE id=12`,
		`UPDATE proto_resources SET status='pending' WHERE id=13`,
		`UPDATE proto_resources SET alloc_bucket=2 WHERE id=14`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	return db
}

func TestProtoSuffixCandidatesAndLockRecheckStayExact(t *testing.T) {
	db := newProtoSuffixInventoryDB(t)
	repo, ctx := NewRepo(db), context.Background()
	for _, test := range []struct {
		suffix string
		ids    []uint
	}{{"", []uint{1, 2, 14}}, {"proton.me", []uint{1, 14}}, {"@PROTONMAIL.COM", []uint{2}}} {
		rows, err := repo.ListProtoSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, nil, 100, test.suffix)
		require.NoError(t, err)
		ids := make([]uint, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ResourceID)
		}
		require.Equal(t, test.ids, ids)
	}
	bucket := uint16(1)
	rows, err := repo.ListProtoSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, &bucket, 100, "proton.me")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 1, rows[0].ResourceID)
	wrong, err := repo.LockProtoCandidate(ctx, 1, 10, 2, domain.SupplyScopePublic, "protonmail.com")
	require.NoError(t, err)
	require.Nil(t, wrong)
	right, err := repo.LockProtoCandidate(ctx, 2, 10, 2, domain.SupplyScopePublic, "protonmail.com")
	require.NoError(t, err)
	require.NotNil(t, right)
	require.EqualValues(t, 2, right.ResourceID)
	for _, suffix := range []string{"@", "proto", "proto.me", "proton.me.", "sub.proton.me", "protonmail.com.evil"} {
		_, err := repo.ListProtoSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, nil, 10, suffix)
		require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
		_, err = repo.LockProtoCandidate(ctx, 1, 10, 2, domain.SupplyScopePublic, suffix)
		require.ErrorIs(t, err, domain.ErrInvalidAllocationRequest)
	}
}

func TestProtoSuffixLockRechecksChangedEligibility(t *testing.T) {
	for name, statement := range map[string]string{
		"suffix":           `UPDATE proto_resources SET email_domain='protonmail.com',email_address='changed@protonmail.com' WHERE id=1`,
		"session revision": `UPDATE proto_sessions SET credential_revision=1 WHERE resource_id=1`,
		"project history":  `INSERT INTO proto_allocations(resource_id,project_id) VALUES (1,10)`,
		"owner status":     `UPDATE users SET status='disabled' WHERE id=1`,
	} {
		t.Run(name, func(t *testing.T) {
			db := newProtoSuffixInventoryDB(t)
			repo, ctx := NewRepo(db), context.Background()
			rows, err := repo.ListProtoSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, nil, 1, "proton.me")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.EqualValues(t, 1, rows[0].ResourceID)
			require.NoError(t, db.Exec(statement).Error)
			row, err := repo.LockProtoCandidate(ctx, 1, 10, 2, domain.SupplyScopePublic, "proton.me")
			require.NoError(t, err)
			require.Nil(t, row)
		})
	}
}

func TestProtoSuffixInventoryUsesTheSameEligibilityForBothScopes(t *testing.T) {
	db := newProtoSuffixInventoryDB(t)
	repo, ctx := NewRepo(db), context.Background()
	config := allocapp.ProductAllocationConfig{ProjectID: 10, ProductID: 20, ProductType: coredomain.ProductTypeProto}
	public, err := repo.ListProductSuffixInventory(ctx, config, 2, domain.SupplyScopePublic)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"proton.me": 2, "protonmail.com": 1}, public)
	private, err := repo.ListProductSuffixInventory(ctx, config, 2, domain.SupplyScopeOwned)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"proton.me": 1, "protonmail.com": 1}, private)
	privateRows, err := repo.ListPrivateProtoInventoryTotals(ctx, 10, 2)
	require.NoError(t, err)
	require.Equal(t, []allocapp.PrivateProductInventoryTotal{{ProductID: 20, Suffix: "proton.me", Available: 1}, {ProductID: 20, Suffix: "protonmail.com", Available: 1}}, privateRows)
	stats, err := repo.GetInventoryStats(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 4, stats.Proto.EligibleResources)
	require.EqualValues(t, 3, stats.Proto.TotalAvailable)
	totals, err := repo.GetProductInventoryTotals(ctx, 10)
	require.NoError(t, err)
	require.Len(t, totals.Items, 1)
	item := totals.Items[0]
	require.EqualValues(t, 3, totals.TotalAvailable)
	require.EqualValues(t, 3, item.TotalAvailable)
	require.EqualValues(t, 3, item.PublicAvailable)
	for _, count := range []*int64{item.CodeAvailable, item.CodePublicAvailable, item.PurchaseAvailable, item.PurchasePublicAvailable} {
		require.NotNil(t, count)
		require.EqualValues(t, 3, *count)
	}
	require.Equal(t, mergeSuffixInventory(public, public), item.Suffixes)
}

func TestProtoSuffixInventoryKeepsBothChildrenWhenOneOrBothAreEmpty(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			db := newProtoSuffixInventoryDB(t)
			available := int64(2)
			if empty {
				require.NoError(t, db.Exec(`UPDATE proto_resources SET status='disabled'`).Error)
				available = 0
			} else {
				require.NoError(t, db.Exec(`UPDATE proto_resources SET for_sale=FALSE WHERE email_domain='protonmail.com'`).Error)
			}
			repo, ctx := NewRepo(db), context.Background()
			suffixes, err := repo.ListProductSuffixInventory(ctx, allocapp.ProductAllocationConfig{ProjectID: 10, ProductID: 20, ProductType: coredomain.ProductTypeProto}, 2, domain.SupplyScopePublic)
			require.NoError(t, err)
			require.Equal(t, map[string]int64{"proton.me": available, "protonmail.com": 0}, suffixes)
			totals, err := repo.GetProductInventoryTotals(ctx, 10)
			require.NoError(t, err)
			require.Len(t, totals.Items, 1)
			require.EqualValues(t, available, totals.TotalAvailable)
			require.Equal(t, []allocapp.ProductInventorySuffixTotal{{Suffix: "proton.me", TotalAvailable: available, PublicAvailable: available}, {Suffix: "protonmail.com"}}, totals.Items[0].Suffixes)
		})
	}
}
