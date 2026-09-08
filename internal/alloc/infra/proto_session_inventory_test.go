package infra

import (
	"context"
	"fmt"
	"testing"

	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProtoInventoryRequiresCurrentSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT, role TEXT)`,
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT, owner_user_id INTEGER)`,
		`CREATE TABLE proto_resources (id INTEGER PRIMARY KEY, resource_type TEXT, owner_user_id INTEGER, email_address TEXT, status TEXT, for_sale BOOLEAN, credential_revision INTEGER, alloc_bucket INTEGER, quality_score INTEGER, last_allocated_at DATETIME)`,
		`CREATE TABLE proto_sessions (resource_id INTEGER PRIMARY KEY, credential_revision INTEGER)`,
		`CREATE TABLE proto_allocations (resource_id INTEGER, project_id INTEGER)`,
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, status TEXT)`,
		`CREATE TABLE project_products (id INTEGER PRIMARY KEY, project_id INTEGER, type TEXT, status TEXT)`,
		`INSERT INTO users VALUES (1, 'active', 'supplier')`,
		`INSERT INTO email_resources VALUES (1, 'proto', 1), (2, 'proto', 1), (3, 'proto', 1)`,
		`INSERT INTO proto_resources VALUES (1, 'proto', 1, 'one@proton.me', 'normal', TRUE, 2, 1, 100, NULL), (2, 'proto', 1, 'two@proton.me', 'normal', TRUE, 2, 2, 100, NULL), (3, 'proto', 1, 'three@proton.me', 'normal', TRUE, 2, 3, 100, NULL)`,
		`INSERT INTO proto_sessions VALUES (2, 1), (3, 2)`,
		`INSERT INTO projects VALUES (10, 'listed')`,
		`INSERT INTO project_products VALUES (20, 10, 'proto', 'enabled')`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	repo := NewRepo(db)
	ctx := context.Background()
	rows, err := repo.ListProtoSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, nil, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint(3), rows[0].ResourceID)
	for _, id := range []uint{1, 2} {
		row, err := repo.LockProtoCandidate(ctx, id, 10, 2, domain.SupplyScopePublic)
		require.NoError(t, err)
		require.Nil(t, row)
	}
	require.NoError(t, db.Exec(`UPDATE proto_resources SET for_sale = FALSE`).Error)
	private, err := repo.ListPrivateProtoInventoryTotals(ctx, 10, 1)
	require.NoError(t, err)
	require.Len(t, private, 1)
	require.EqualValues(t, 1, private[0].Available)
}

func TestProtoExistingAllocationRequiresCurrentSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT, owner_user_id INTEGER)`,
		`CREATE TABLE proto_resources (id INTEGER PRIMARY KEY, resource_type TEXT, owner_user_id INTEGER, email_address TEXT, status TEXT, credential_revision INTEGER)`,
		`CREATE TABLE proto_sessions (resource_id INTEGER PRIMARY KEY, credential_revision INTEGER)`,
		`CREATE TABLE proto_allocations (id INTEGER PRIMARY KEY, resource_id INTEGER, order_no TEXT, guard_type TEXT, status TEXT, email TEXT)`,
		`INSERT INTO email_resources VALUES (1,'proto',7),(2,'proto',7),(3,'proto',7),(4,'proto',7)`,
		`INSERT INTO proto_resources VALUES (1,'proto',7,'one@proton.me','normal',2),(2,'proto',7,'two@proton.me','normal',2),(3,'proto',7,'three@proton.me','normal',2),(4,'proto',7,'four@proton.me','identifying',2)`,
		`INSERT INTO proto_sessions VALUES (2,1),(3,2),(4,2)`,
		`INSERT INTO proto_allocations VALUES (11,1,'ONE','proto','allocated','one@proton.me'),(12,2,'TWO','proto','allocated','two@proton.me'),(13,3,'THREE','proto','allocated','three@proton.me'),(14,4,'FOUR','proto','allocated','four@proton.me')`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	repo := NewRepo(db)
	for _, test := range []struct {
		order string
		id    uint
		ready bool
	}{{"ONE", 0, false}, {"ONE", 11, false}, {"TWO", 12, false}, {"THREE", 0, true}, {"THREE", 13, true}, {"THREE", 14, false}, {"FOUR", 14, false}, {"NEW", 0, true}, {"NEW", 13, false}} {
		ready, err := repo.ProtoAllocationReady(context.Background(), test.order, test.id)
		require.NoError(t, err)
		require.Equal(t, test.ready, ready, test.order)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	ready, err := repo.ProtoAllocationReady(context.Background(), "NEW", 0)
	require.Error(t, err)
	require.False(t, ready, "a database error must not look like a new unresolved allocation")
}
