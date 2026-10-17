package infra

import (
	"context"
	"testing"
	"time"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	daumdomain "github.com/donnel666/remail/internal/daum/domain"
	dauminfra "github.com/donnel666/remail/internal/daum/infra"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type daumHistoryRoot struct {
	ID          uint `gorm:"primaryKey"`
	Type        string
	OwnerUserID uint
	Version     uint64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (daumHistoryRoot) TableName() string { return "email_resources" }

func TestDaumCorrectedIdentityImportsNewHistoryAndReplaysLegacyFacts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&daumHistoryRoot{}, &dauminfra.Resource{}, &dauminfra.ExplicitAlias{}, &dauminfra.BindingMailbox{}, &dauminfra.MaintenanceRun{}, &DaumAllocationModel{}, &OrderGuardModel{}))
	ctx := context.Background()
	service := dauminfra.NewService(db)
	id, _, err := service.ImportLine(ctx, 7, daumdomain.ImportLine{Email: "old@daum.net", AppPassword: "fixture"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&dauminfra.Resource{}).Where("id=?", id).Update("status", "identifying").Error)
	repo := NewRepo(db)
	allocator := allocapp.NewUseCase(repo)
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	legacyNo := "HIST-DAUM-1-4-daum.net"
	require.EqualValues(t, 1, id)
	legacy := &domain.DaumAllocation{OrderNo: legacyNo, ProjectID: 4, ProductID: 5, ResourceID: id, OwnerUserID: 7, SupplyScope: domain.SupplyScopePublic, ServiceMode: "purchase", Mailbox: "main", Email: "old@daum.net", Status: domain.AllocationStatusReleased, CostPointsSnapshot: "0.00", CreatedAt: now, ReleasedAt: &now}
	require.NoError(t, repo.WithTx(ctx, func(txCtx context.Context) error {
		if err := repo.CreateOrderGuard(txCtx, legacyNo, domain.AllocationTypeDaum); err != nil {
			return err
		}
		return repo.CreateDaumAllocation(txCtx, legacy)
	}))
	cmd := allocapp.HistoricalDaumAllocationCommand{ProjectID: 4, ProductID: 5, ResourceID: id, Email: "OLD@DAUM.NET", CreatedAt: now, ReleasedAt: now}
	old, err := allocator.ImportHistoricalDaumAllocation(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, legacyNo, old.OrderNo)
	require.False(t, old.Created)
	cmd.Email = "old@hanmail.net"
	oldAlias, err := allocator.ImportHistoricalDaumAllocation(ctx, cmd)
	require.NoError(t, err)
	require.True(t, oldAlias.Created)
	corrected := "corrected@daum.net"
	require.NoError(t, service.UpdateResource(ctx, id, &corrected, nil))
	appPassword := "corrected-fixture"
	require.NoError(t, service.ReplaceCredentials(ctx, id, nil, nil, &appPassword, nil))
	require.NoError(t, db.Model(&dauminfra.Resource{}).Where("id=?", id).Update("status", "identifying").Error)
	for _, email := range []string{corrected, "corrected@hanmail.net"} {
		cmd.Email = email
		fresh, err := allocator.ImportHistoricalDaumAllocation(ctx, cmd)
		require.NoError(t, err)
		require.True(t, fresh.Created)
		require.Equal(t, email, fresh.Email)
		require.NotEqual(t, legacyNo, fresh.OrderNo)
		require.NotEqual(t, oldAlias.OrderNo, fresh.OrderNo)
		require.LessOrEqual(t, len(fresh.OrderNo), 64)
		replay, err := allocator.ImportHistoricalDaumAllocation(ctx, cmd)
		require.NoError(t, err)
		require.Equal(t, fresh.OrderNo, replay.OrderNo)
		require.False(t, replay.Created)
	}
	for _, fact := range []*domain.UnifiedAllocation{old, oldAlias} {
		stored, err := repo.FindAllocationByOrder(ctx, fact.OrderNo)
		require.NoError(t, err)
		require.Equal(t, fact.Email, stored.Email)
		require.Equal(t, domain.AllocationStatusReleased, stored.Status)
	}
	var count int64
	require.NoError(t, db.Model(&DaumAllocationModel{}).Count(&count).Error)
	require.EqualValues(t, 4, count)
}
