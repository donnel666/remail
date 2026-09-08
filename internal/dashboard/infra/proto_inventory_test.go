package infra

import (
	"context"
	"fmt"
	"testing"

	dashboardapp "github.com/donnel666/remail/internal/dashboard/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProtoDashboardInventoryRequiresCurrentSessionWithoutChangingOtherProviders(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT, role TEXT)`,
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT, owner_user_id INTEGER)`,
		`CREATE TABLE microsoft_resources (status TEXT, for_sale BOOLEAN, graph_available BOOLEAN)`,
		`CREATE TABLE generated_mailboxes (status TEXT)`,
		`CREATE TABLE gmail_resources (id INTEGER PRIMARY KEY, status TEXT, for_sale BOOLEAN)`,
		`CREATE TABLE icloud_resources (id INTEGER PRIMARY KEY, status TEXT, for_sale BOOLEAN)`,
		`CREATE TABLE icloud_aliases (id INTEGER PRIMARY KEY, resource_id INTEGER, status TEXT, forward_to_email TEXT)`,
		`CREATE TABLE icloud_allocations (alias_id INTEGER, status TEXT)`,
		`CREATE TABLE domain_resources (domain TEXT, purpose TEXT, status TEXT)`,
		`CREATE TABLE proto_resources (id INTEGER PRIMARY KEY, owner_user_id INTEGER, status TEXT, for_sale BOOLEAN, credential_revision INTEGER)`,
		`CREATE TABLE proto_sessions (resource_id INTEGER PRIMARY KEY, credential_revision INTEGER)`,
		`INSERT INTO users VALUES (1, 'active', 'supplier')`,
		`INSERT INTO email_resources VALUES (1, 'proto', 1), (2, 'proto', 1), (3, 'proto', 1), (10, 'gmail', 1), (11, 'gmail', 1)`,
		`INSERT INTO microsoft_resources VALUES ('normal', TRUE, TRUE), ('normal', FALSE, TRUE), ('deleted', FALSE, FALSE)`,
		`INSERT INTO generated_mailboxes VALUES ('normal'), ('disabled'), ('retired')`,
		`INSERT INTO gmail_resources VALUES (10, 'normal', TRUE), (11, 'normal', FALSE), (12, 'deleted', FALSE)`,
		`INSERT INTO icloud_resources VALUES (20, 'normal', TRUE), (21, 'normal', FALSE), (22, 'deleted', FALSE)`,
		`INSERT INTO icloud_aliases VALUES (30, 20, 'normal', 'inbox@relay.example')`,
		`INSERT INTO domain_resources VALUES ('relay.example', 'binding', 'normal')`,
		`INSERT INTO proto_resources VALUES (1, 1, 'normal', TRUE, 2), (2, 1, 'normal', TRUE, 2), (3, 1, 'normal', TRUE, 2)`,
		// Resource 1 has no session; resource 2 only has an obsolete revision.
		`INSERT INTO proto_sessions VALUES (2, 1), (3, 2)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	previousSuffixes := runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, "")
	runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, "relay.example")
	t.Cleanup(func() {
		if previousSuffixes == "" {
			runtimeconfig.Delete(runtimeconfig.ICloudForwardingSuffixesKey)
		} else {
			runtimeconfig.Set(runtimeconfig.ICloudForwardingSuffixesKey, previousSuffixes)
		}
	})
	want := dashboardapp.InventorySnapshot{
		MicrosoftTotal: 2, MicrosoftAvailable: 1,
		DomainTotal: 2, DomainAvailable: 1,
		GmailTotal: 2, GmailAvailable: 1,
		ICloudTotal: 2, ICloudAvailable: 1,
		ProtoTotal: 3, ProtoAvailable: 1,
	}
	repo := NewAdminViewRepo(db)
	snapshot, err := repo.InventorySnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, snapshot)
	require.NoError(t, db.Exec(`DELETE FROM proto_sessions WHERE resource_id = 3`).Error)
	want.ProtoAvailable = 0
	snapshot, err = repo.InventorySnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, snapshot)
}
