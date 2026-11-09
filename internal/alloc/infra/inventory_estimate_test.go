package infra

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFastInventorySuffixesSubtractHistoryAndSort(t *testing.T) {
	base := &fastInventoryBase{AliasesBySuffix: map[string]int64{
		"hotmail.com": 2,
		"outlook.com": 10,
	}}
	history := &fastInventoryHistory{AliasBySuffix: map[string]int64{
		"outlook.com": 3,
	}}

	got := fastInventorySuffixes(base, history)
	require.Equal(t, []allocapp.ProductInventorySuffixTotal{
		{Suffix: "hotmail.com", TotalAvailable: 2, PublicAvailable: 2},
		{Suffix: "outlook.com", TotalAvailable: 7, PublicAvailable: 7},
	}, got)
}

func TestFastInventoryHistoryUsesRedisWatermarkBatches(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE microsoft_allocations (
id INTEGER PRIMARY KEY, created_at DATETIME NOT NULL, project_id INTEGER NOT NULL,
mailbox TEXT NOT NULL, resource_id INTEGER, explicit_alias_id INTEGER,
dot_alias_id INTEGER, plus_alias_id INTEGER
)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE explicit_aliases (id INTEGER PRIMARY KEY, email_domain TEXT NOT NULL)`).Error)
	base := time.Date(2026, time.November, 9, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec("INSERT INTO explicit_aliases(id,email_domain) VALUES (10,'outlook.com'),(11,'hotmail.com')").Error)
	require.NoError(t, db.Exec(`INSERT INTO microsoft_allocations
(id,created_at,project_id,mailbox,resource_id,explicit_alias_id,dot_alias_id,plus_alias_id)
VALUES (1,?,?,?,?,?,?,?), (2,?,?,?,?,?,?,?), (3,?,?,?,?,?,?,?)`,
		base, 7, "main", 101, nil, nil, nil,
		base.Add(time.Minute), 7, "alias", 101, 10, nil, nil,
		base.Add(2*time.Minute), 7, "dot", 101, nil, 20, nil).Error)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo := &Repo{db: db, redis: client}
	history, err := repo.loadFastInventoryHistory(context.Background(), 7)
	require.NoError(t, err)
	require.EqualValues(t, 3, history.MaxAllocationID)
	require.EqualValues(t, 1, history.MainUsed)
	require.EqualValues(t, 1, history.DotUsed)
	require.EqualValues(t, 1, history.AliasBySuffix["outlook.com"])

	// A second read uses the stored watermark and does not duplicate HLL values.
	again, err := repo.loadFastInventoryHistory(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, history.AliasBySuffix, again.AliasBySuffix)
}
