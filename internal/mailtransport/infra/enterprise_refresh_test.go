package infra

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftEnterpriseTokenDispatchDoesNotDependOnPersonalTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Table("microsoft_enterprise_resources").AutoMigrate(&MicrosoftTokenRefreshStateModel{}))
	now := time.Now()
	require.NoError(t, db.Table("microsoft_enterprise_resources").Create(&MicrosoftTokenRefreshStateModel{ResourceID: 1, ResourceType: "microsoft_enterprise", Status: "pending", Generation: 1, RequestedAt: &now}).Error)
	repo := NewMicrosoftTokenRefreshRepo(db)
	pending, err := repo.ListPending(context.Background(), 1, true)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.True(t, pending[0].Enterprise)
	require.False(t, db.Migrator().HasTable("microsoft_resources"))
}
