package infra

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestListRecentInventoryOrderActivitySamplesLatestRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE orders (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,
  created_at DATETIME NOT NULL
)`).Error)

	base := time.Date(2026, time.November, 7, 0, 0, 0, 0, time.UTC)
	type orderRow struct {
		id        int
		projectID int
		createdAt time.Time
	}
	rows := []orderRow{
		{id: 1, projectID: 10, createdAt: base},
		{id: 2, projectID: 11, createdAt: base.Add(time.Minute)},
		{id: 3, projectID: 11, createdAt: base.Add(2 * time.Minute)},
	}
	for id := 4; id <= 100; id++ {
		rows = append(rows, orderRow{id: id, projectID: 20, createdAt: base.Add(time.Duration(id) * time.Minute)})
	}
	rows = append(rows, orderRow{id: 101, projectID: 30, createdAt: base.Add(101 * time.Minute)})
	for _, row := range rows {
		require.NoError(t, db.Exec("INSERT INTO orders(id, project_id, created_at) VALUES (?, ?, ?)", row.id, row.projectID, row.createdAt).Error)
	}

	activity, err := NewRepo(db).ListRecentInventoryOrderActivity(context.Background(), 100)
	require.NoError(t, err)
	byProject := make(map[uint]time.Time, len(activity))
	for _, item := range activity {
		byProject[item.ProjectID] = item.OrderedAt
	}
	require.NotContains(t, byProject, uint(10), "the oldest project-10 row is outside the latest-100 sample")
	require.Equal(t, base.Add(2*time.Minute), byProject[11])
	require.Equal(t, base.Add(101*time.Minute), byProject[30])
	require.Equal(t, base.Add(100*time.Minute), byProject[20])

	// The latest-100 sample keeps the newest row returned for each project.
	activity, err = NewRepo(db).ListRecentInventoryOrderActivity(context.Background(), 101)
	require.NoError(t, err)
	byProject = make(map[uint]time.Time, len(activity))
	for _, item := range activity {
		byProject[item.ProjectID] = item.OrderedAt
	}
	require.Equal(t, base, byProject[10])
	require.Equal(t, base.Add(2*time.Minute), byProject[11])
}
