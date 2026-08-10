package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestICloudResourceFetchScopeDoesNotCollapseAliasesIntoOneOrder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:mailmatch-icloud-resource-fetch?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT NOT NULL);
CREATE TABLE icloud_resources (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL,
    primary_email TEXT NOT NULL,
    credential_revision INTEGER NOT NULL
);
CREATE TABLE icloud_allocations (
    id INTEGER PRIMARY KEY,
    resource_id INTEGER NOT NULL,
    order_no TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
INSERT INTO email_resources(id, type) VALUES (10, 'icloud');
INSERT INTO icloud_resources(id, status, primary_email, credential_revision)
VALUES (10, 'normal', 'main@icloud.com', 4);
INSERT INTO icloud_allocations(id, resource_id, order_no, status, created_at) VALUES
    (1, 10, 'released-order', 'released', '2026-08-01 00:00:00'),
    (2, 10, 'active-order', 'allocated', '2026-08-02 00:00:00');
`).Error)

	repo := NewAdminResourceFetchRepo(db)
	scope, err := repo.LoadResourceFetchScope(context.Background(), 10, 4, domain.ResourceTypeICloud)
	require.NoError(t, err)
	require.Empty(t, scope.OrderNo)
	require.Equal(t, domain.ResourceTypeICloud, scope.ResourceType)
	_, err = repo.LoadResourceFetchScope(context.Background(), 10, 999, domain.ResourceTypeICloud)
	require.NoError(t, err, "domain mailbox fetch must not depend on Apple credential revisions")

	require.NoError(t, db.Table("icloud_allocations").Where("id = ?", 2).Update("status", "released").Error)
	scope, err = repo.LoadResourceFetchScope(context.Background(), 10, 4, domain.ResourceTypeICloud)
	require.NoError(t, err)
	require.Empty(t, scope.OrderNo)
	require.NoError(t, db.Table("icloud_resources").Where("id = ?", 10).Update("status", "disabled").Error)
	_, err = repo.LoadResourceFetchScope(context.Background(), 10, 4, domain.ResourceTypeICloud)
	require.NoError(t, err, "disabled supply must not hide persisted alias mail from administrators")
}
