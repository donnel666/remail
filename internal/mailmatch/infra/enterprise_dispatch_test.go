package infra

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftEnterpriseHistoryDispatchHasOwnAllowance(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&FetchStateModel{}))
	older, newer := time.Now().Add(-time.Hour), time.Now()
	require.NoError(t, db.Create(&[]FetchStateModel{
		{EmailResourceID: 1, Status: "pending", Generation: 1, OperationKind: "resource_history", RequestedAt: &older},
		{EmailResourceID: 2, Status: "pending", Generation: 1, OperationKind: "enterprise_history", RequestedAt: &newer},
	}).Error)
	repo := NewResourceHistoryRepo(db)
	for _, enterprise := range []bool{true, false} {
		jobs, err := repo.ListPendingResourceFetches(context.Background(), 1, enterprise)
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		require.Equal(t, enterprise, jobs[0].ResourceType == domain.ResourceTypeMicrosoftEnterprise)
	}
}
