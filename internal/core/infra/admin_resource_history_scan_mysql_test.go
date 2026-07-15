package infra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminResourceRepoFindsMicrosoftHistoryScanRangeMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		8200, "history-owner@test.local", "hash", "supplier",
	).Error)
	for _, id := range []uint{8201, 8202, 8203} {
		require.NoError(t, db.Exec(
			"INSERT INTO email_resources(id, type, owner_user_id) VALUES (?, 'microsoft', ?)", id, 8200,
		).Error)
	}
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(id, email_address, password, client_id, refresh_token, status)
VALUES
    (8201, 'history-1@test.local', 'password', 'client', 'refresh', 'disabled'),
    (8202, 'history-deleted@test.local', 'password', 'client', 'refresh', 'deleted'),
    (8203, 'history-3@test.local', 'password', 'client', 'refresh', 'normal')`).Error)

	repo := NewAdminResourceRepo(db)
	maxID, err := repo.MaxMicrosoftResourceID(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint(8203), maxID)
	first, err := repo.FindNextMicrosoft(context.Background(), 0, maxID)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, uint(8201), first.ID)
	second, err := repo.FindNextMicrosoft(context.Background(), first.ID, maxID)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, uint(8203), second.ID)
	none, err := repo.FindNextMicrosoft(context.Background(), second.ID, maxID)
	require.NoError(t, err)
	require.Nil(t, none)
}
