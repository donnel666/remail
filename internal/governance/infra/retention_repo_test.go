package infra

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRetentionRepoRedactsOnlyExpiredTerminalGmailCodes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-code-retention?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE gmail_code_sessions (
		id INTEGER PRIMARY KEY,
		status TEXT NOT NULL,
		completed_at DATETIME,
		codes_json JSON NOT NULL
	)`).Error)
	now := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		id          int
		status      string
		completedAt time.Time
	}{
		{id: 1, status: "completed", completedAt: now.AddDate(0, 0, -4)},
		{id: 2, status: "completed", completedAt: now.AddDate(0, 0, -2)},
		{id: 3, status: "active", completedAt: now.AddDate(0, 0, -4)},
	} {
		require.NoError(t, db.Exec(
			"INSERT INTO gmail_code_sessions(id, status, completed_at, codes_json) VALUES (?, ?, ?, ?)",
			row.id, row.status, row.completedAt, `[{"seq":1,"code":"123456"}]`,
		).Error)
	}

	repo := NewRetentionRepo(db)
	affected, err := repo.RedactGmailCodesBefore(context.Background(), now.AddDate(0, 0, -3), 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, affected)

	var values []string
	require.NoError(t, db.Table("gmail_code_sessions").Order("id").Pluck("codes_json", &values).Error)
	require.Equal(t, []string{"[]", `[{"seq":1,"code":"123456"}]`, `[{"seq":1,"code":"123456"}]`}, values)
}
