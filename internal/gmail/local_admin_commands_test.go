package gmail

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocalGmailAdminCommandsAreIdempotentAndGuardPublicOwner(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	db, err := gorm.Open(sqlite.Open("file:gmail-admin-command-idempotency?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&resourceRootModel{}, &localResourceModel{}, &governanceinfra.OperationLogModel{}))
	require.NoError(t, db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT NOT NULL, role TEXT NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO users(id, status, role) VALUES (7, 'active', 'user')").Error)
	root := resourceRootModel{Type: "gmail", OwnerUserID: 7, Version: 5}
	require.NoError(t, db.Create(&root).Error)
	require.NoError(t, db.Create(&localResourceModel{
		ID: root.ID, ResourceType: "gmail", OwnerUserID: 7, Email: "commands@gmail.com", Identity: "commands@gmail.com",
		Password: "password", TwoFactorSecret: "JBSWY3DPEHPK3PXP", AppPassword: "app-password",
		CredentialRevision: 1, ValidationGeneration: 1, Status: LocalResourceDisabled,
	}).Error)
	service := NewService(db, nil)
	service.redis = redisClient

	require.NoError(t, service.SetAdminLocalResourceEnabled(
		context.Background(), root.ID, 5, true, 9, "enable-key", "request-enable", "/enable",
	))
	require.NoError(t, service.SetAdminLocalResourceEnabled(
		context.Background(), root.ID, 5, true, 9, "enable-key", "request-enable-replay", "/enable",
	))
	require.ErrorIs(t, service.SetAdminLocalResourceEnabled(
		context.Background(), root.ID, 5, false, 9, "enable-key", "request-conflict", "/disable",
	), ErrLocalValidationConflict)

	var resource localResourceModel
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.Equal(t, LocalResourcePending, resource.Status)
	require.EqualValues(t, 2, resource.ValidationGeneration)
	require.NoError(t, db.First(&root, root.ID).Error)
	require.EqualValues(t, 6, root.Version)

	err = service.SetAdminLocalResourceForSale(
		context.Background(), root.ID, 6, true, 9, "publish-key", "request-publish", "/publish",
	)
	require.ErrorIs(t, err, ErrInvalidLocalResource)
	require.NoError(t, db.Exec("UPDATE users SET role = 'supplier' WHERE id = 7").Error)
	require.NoError(t, service.SetAdminLocalResourceForSale(
		context.Background(), root.ID, 6, true, 9, "publish-key", "request-publish-retry", "/publish",
	))
	require.NoError(t, service.SetAdminLocalResourceForSale(
		context.Background(), root.ID, 6, true, 9, "publish-key", "request-publish-replay", "/publish",
	))
	require.NoError(t, db.First(&resource, root.ID).Error)
	require.True(t, resource.ForSale)
	require.NoError(t, db.First(&root, root.ID).Error)
	require.EqualValues(t, 7, root.Version)

	var logCount int64
	require.NoError(t, db.Model(&governanceinfra.OperationLogModel{}).Count(&logCount).Error)
	require.EqualValues(t, 2, logCount)
}
