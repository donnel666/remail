package infra

import (
	"context"
	"fmt"
	"testing"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftHistoryCursorSelectsOnlyProjectAccountTypes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	for _, statement := range []string{
		`CREATE TABLE projects(id INTEGER PRIMARY KEY, status TEXT)`,
		`CREATE TABLE project_products(project_id INTEGER, type TEXT, status TEXT)`,
		`CREATE TABLE project_mail_rules(project_id INTEGER, enabled BOOLEAN)`,
		`CREATE TABLE microsoft_resources(id INTEGER PRIMARY KEY, resource_type TEXT DEFAULT 'microsoft', status TEXT, email_address TEXT, email_domain TEXT, client_id TEXT DEFAULT 'client', refresh_token TEXT DEFAULT 'token', graph_available BOOLEAN DEFAULT TRUE, credential_revision INTEGER DEFAULT 1)`,
		`CREATE TABLE microsoft_enterprise_resources(id INTEGER PRIMARY KEY, resource_type TEXT DEFAULT 'microsoft_enterprise', status TEXT, email_address TEXT, email_domain TEXT, client_id TEXT DEFAULT 'client', refresh_token TEXT DEFAULT 'token', graph_available BOOLEAN DEFAULT TRUE, credential_revision INTEGER DEFAULT 1)`,
		`INSERT INTO projects VALUES(1,'listed'),(2,'listed'),(3,'listed'),(4,'listed'),(5,'listed'),(6,'reviewing'),(7,'delisted'),(8,'listed')`,
		`INSERT INTO project_products VALUES(1,'microsoft','enabled'),(2,'microsoft_enterprise','disabled'),(3,'microsoft','enabled'),(3,'microsoft_enterprise','enabled'),(4,'domain','enabled'),(6,'microsoft','enabled'),(7,'microsoft','disabled'),(8,'microsoft','enabled')`,
		`INSERT INTO project_mail_rules VALUES(1,TRUE),(2,TRUE),(3,TRUE),(4,TRUE),(5,TRUE),(6,TRUE),(7,TRUE),(8,FALSE)`,
		`INSERT INTO microsoft_enterprise_resources(id,status,email_address,email_domain) VALUES(101,'normal','main@tenant.onmicrosoft.com','tenant.onmicrosoft.com'),(102,'disabled','disabled@tenant.onmicrosoft.com','tenant.onmicrosoft.com'),(103,'deleted','deleted@tenant.onmicrosoft.com','tenant.onmicrosoft.com'),(104,'normal','later@tenant.onmicrosoft.com','tenant.onmicrosoft.com')`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	for id := 1; id <= 100; id++ {
		require.NoError(t, db.Exec("INSERT INTO microsoft_resources(id,status,email_address,email_domain) VALUES(?,'normal',?,'outlook.com')", id, fmt.Sprintf("user%d@outlook.com", id)).Error)
	}
	repo := NewAdminResourceRepo(db)
	ctx := context.Background()
	personalMax, err := repo.MaxMicrosoftResourceID(ctx, false)
	require.NoError(t, err)
	require.EqualValues(t, 100, personalMax)
	enterpriseMax, err := repo.MaxMicrosoftResourceID(ctx, true)
	require.NoError(t, err)
	require.EqualValues(t, 104, enterpriseMax)
	for _, tc := range []struct{ project, after, through, want uint }{
		{1, 0, 103, 1}, {1, 100, 103, 0},
		{2, 0, 100, 0}, {2, 0, 103, 101}, {2, 101, 103, 102}, {2, 102, 103, 0},
		{3, 99, 103, 100}, {3, 100, 103, 101},
		{4, 0, 103, 0}, {5, 0, 103, 0}, {6, 0, 103, 0}, {7, 0, 103, 1}, {8, 0, 103, 0},
	} {
		resource, err := repo.FindNextMicrosoft(ctx, tc.after, tc.through, tc.project, tc.project == 2 || tc.project == 3 && tc.after >= 100)
		require.NoError(t, err)
		if tc.want == 0 {
			require.Nil(t, resource, "%+v", tc)
		} else {
			require.NotNil(t, resource)
			require.Equal(t, tc.want, resource.ID, "%+v", tc)
		}
	}
	scope, err := coreapp.NewMicrosoftCredentialService(repo).FindNextMicrosoftCredentialScope(ctx, 0, 103, 2, true)
	require.NoError(t, err)
	require.Equal(t, uint(101), scope.ResourceID)
}
