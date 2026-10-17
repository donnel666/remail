package infra

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftAllocationReadinessRejectsChangedOrUnavailableAddress(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.Exec(`
 CREATE TABLE email_resources(id INTEGER,type TEXT);
 CREATE TABLE microsoft_resources(id INTEGER,status TEXT,email_address TEXT);
 CREATE TABLE explicit_aliases(id INTEGER,resource_id INTEGER,email TEXT,status TEXT);
 CREATE TABLE dot_aliases(id INTEGER,resource_id INTEGER,email TEXT,status TEXT);
 CREATE TABLE plus_aliases(id INTEGER,resource_id INTEGER,email TEXT,status TEXT);
 CREATE TABLE microsoft_allocations(id INTEGER,order_no TEXT,status TEXT,resource_id INTEGER,mailbox TEXT,email TEXT,explicit_alias_id INTEGER,dot_alias_id INTEGER,plus_alias_id INTEGER);
 INSERT INTO email_resources VALUES(7,'microsoft');
 INSERT INTO microsoft_resources VALUES(7,'normal','one@outlook.com');
 INSERT INTO explicit_aliases VALUES(8,7,'alias@outlook.com','normal');
 INSERT INTO dot_aliases VALUES(9,7,'o.ne@outlook.com','normal');
 INSERT INTO plus_aliases VALUES(10,7,'one+test@outlook.com','normal');
 INSERT INTO microsoft_allocations VALUES
 (1,'MAIN','allocated',7,'main','one@outlook.com',NULL,NULL,NULL),
 (2,'ALIAS','allocated',7,'alias','alias@outlook.com',8,NULL,NULL),
 (3,'DOT','allocated',7,'dot','o.ne@outlook.com',NULL,9,NULL),
 (4,'PLUS','allocated',7,'plus','one+test@outlook.com',NULL,NULL,10);
 `).Error)
	repo := NewRepo(db)
	ctx := context.Background()
	for i, no := range []string{"MAIN", "ALIAS", "DOT", "PLUS"} {
		ready, err := repo.MicrosoftAllocationReady(ctx, no, uint(i+1))
		require.NoError(t, err)
		require.True(t, ready)
	}
	for _, change := range []struct{ sql, restore, no string }{
		{"UPDATE microsoft_resources SET status='abnormal'", "UPDATE microsoft_resources SET status='normal'", "MAIN"},
		{"UPDATE microsoft_resources SET email_address='changed@outlook.com'", "UPDATE microsoft_resources SET email_address='one@outlook.com'", "MAIN"},
		{"UPDATE explicit_aliases SET status='abnormal'", "UPDATE explicit_aliases SET status='normal'", "ALIAS"},
		{"UPDATE explicit_aliases SET resource_id=99", "UPDATE explicit_aliases SET resource_id=7", "ALIAS"},
		{"UPDATE dot_aliases SET email='wrong@outlook.com'", "UPDATE dot_aliases SET email='o.ne@outlook.com'", "DOT"},
		{"UPDATE microsoft_allocations SET status='released' WHERE order_no='PLUS'", "UPDATE microsoft_allocations SET status='allocated' WHERE order_no='PLUS'", "PLUS"},
	} {
		require.NoError(t, db.Exec(change.sql).Error)
		ready, err := repo.MicrosoftAllocationReady(ctx, change.no, 0)
		require.NoError(t, err)
		require.False(t, ready, change.sql)
		require.NoError(t, db.Exec(change.restore).Error)
	}
	ready, err := repo.MicrosoftAllocationReady(ctx, "NEW", 0)
	require.NoError(t, err)
	require.True(t, ready, "an accepted order without an allocation may allocate")
	ready, err = repo.MicrosoftAllocationReady(ctx, "NEW", 1)
	require.NoError(t, err)
	require.False(t, ready, "an existing allocation ID must resolve")
}
