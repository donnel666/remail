package api

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type dashboardInventoryRepoStub struct {
	allocapp.Repository
	available map[uint]int64
	calls     int
}

func (s *dashboardInventoryRepoStub) GetInventoryStats(_ context.Context, projectID uint) (*allocapp.InventoryStats, error) {
	s.calls++
	return &allocapp.InventoryStats{
		ProjectID: projectID, TotalAvailable: s.available[projectID],
		Microsoft: allocapp.MicrosoftInventoryStats{Enabled: true},
	}, nil
}

func (*dashboardInventoryRepoStub) ListInventoryProjectIDs(context.Context) ([]uint, error) {
	return nil, nil
}

func TestProjectInventoryRankingSkipsColdPrivateProjectUntilRefresh(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:dashboard-inventory-ranking?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT, status TEXT, access_type TEXT)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO projects (id, name, status, access_type) VALUES
    (1, 'Public', 'listed', 'public'),
    (2, 'kick', 'listed', 'private'),
    (3, 'Old', 'delisted', 'private')`).Error)

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &dashboardInventoryRepoStub{available: map[uint]int64{1: 11, 2: 7, 3: 1}}
	useCase := allocapp.NewUseCase(repo)
	useCase.SetInventoryCache(allocinfra.NewInventoryCache(client))
	directory := dashboardInventoryDirectory{db: db, alloc: useCase}

	items, err := directory.ProjectInventoryRanking(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Zero(t, repo.calls, "cold dashboard reads must not run aggregate SQL")

	result, err := useCase.RefreshInventoryCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	require.Equal(t, 2, repo.calls, "delisted projects must not be refreshed")

	items, err = directory.ProjectInventoryRanking(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "kick", items[0].Name)
	require.Equal(t, 7, items[0].Available)
	require.Equal(t, "Public", items[1].Name)
	require.Equal(t, 11, items[1].Available)
}
