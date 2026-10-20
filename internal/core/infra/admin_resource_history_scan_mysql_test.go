package infra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminResourceRepoFindsMicrosoftHistoryScanRangeMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec("INSERT INTO projects(id, name, target_platform, status) VALUES (7, 'History', 'history', 'listed')").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(project_id, type, status, code_enabled, main_weight) VALUES (7, 'microsoft', 'enabled', TRUE, 1)").Error)
	require.NoError(t, db.Exec("INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES (7, 'sender', '.*', TRUE)").Error)
	require.NoError(t, db.Exec("INSERT INTO projects(id, name, target_platform, status) VALUES (8, 'Enterprise history', 'history', 'listed')").Error)
	require.NoError(t, db.Exec("INSERT INTO project_products(project_id, type, status, code_enabled, main_weight) VALUES (8, 'microsoft_enterprise', 'enabled', TRUE, 1)").Error)
	require.NoError(t, db.Exec("INSERT INTO project_mail_rules(project_id, rule_type, pattern, enabled) VALUES (8, 'sender', '.*', TRUE)").Error)
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
	first, err := repo.FindNextMicrosoft(context.Background(), 0, maxID, 7)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, uint(8201), first.ID)
	second, err := repo.FindNextMicrosoft(context.Background(), first.ID, maxID, 7)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, uint(8203), second.ID)
	none, err := repo.FindNextMicrosoft(context.Background(), second.ID, maxID, 7)
	require.NoError(t, err)
	require.Nil(t, none)

	noEnterprise, err := repo.FindNextMicrosoft(context.Background(), 0, maxID, 8)
	require.NoError(t, err)
	require.Nil(t, noEnterprise, "personal inventory must not enter an enterprise-only scan")
	require.NoError(t, db.Exec("INSERT INTO email_resources(id, type, owner_user_id) VALUES (8204, 'microsoft', 8200)").Error)
	require.NoError(t, db.Exec("INSERT INTO microsoft_resources(id, email_address, email_domain, password, client_id, refresh_token, status) VALUES (8204, 'main@tenant.onmicrosoft.com', 'tenant.onmicrosoft.com', 'password', 'client', 'refresh', 'normal')").Error)
	noEnterprise, err = repo.FindNextMicrosoft(context.Background(), 0, maxID, 8)
	require.NoError(t, err)
	require.Nil(t, noEnterprise, "the existing scan must keep its resource high-water mark")
	enterprise, err := repo.FindNextMicrosoft(context.Background(), 0, 8204, 8)
	require.NoError(t, err)
	require.NotNil(t, enterprise)
	require.Equal(t, uint(8204), enterprise.ID)
	none, err = repo.FindNextMicrosoft(context.Background(), 8203, 8204, 7)
	require.NoError(t, err)
	require.Nil(t, none, "enterprise inventory must not enter a personal-only scan")

	require.NoError(t, db.Exec("INSERT INTO project_products(project_id, type, status, code_enabled, main_weight) VALUES (8, 'microsoft', 'disabled', TRUE, 1)").Error)
	personal, err := repo.FindNextMicrosoft(context.Background(), 0, 8204, 8)
	require.NoError(t, err)
	require.Equal(t, uint(8201), personal.ID, "disabled products still retain project history")
}
