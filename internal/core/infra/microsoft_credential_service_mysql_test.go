package infra

import (
	"context"
	"errors"
	"testing"
	"time"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMicrosoftCredentialServiceJoinsCallerTransactionMySQL(t *testing.T) {
	db := newCoreMySQLTestDB(t)
	require.NoError(t, db.Exec(
		"INSERT INTO users(id, email, password_hash, role) VALUES (?, ?, ?, ?)",
		8101, "credential-owner@test.local", "hash", "supplier",
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO email_resources(id, type, owner_user_id, version) VALUES (?, ?, ?, ?)",
		8101, "microsoft", 8101, 1,
	).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(
    id, email_address, email_domain, password, client_id, refresh_token,
    credential_revision, credential_updated_at, status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		8101, "credential@test.local", "test.local", "password", "client-v1", "refresh-v1",
		3, time.Now().UTC(), "disabled",
	).Error)

	service := coreapp.NewMicrosoftCredentialService(NewAdminResourceRepo(db))
	forcedRollback := errors.New("forced rollback")
	err := db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		txCtx := platform.WithGormTx(context.Background(), tx)
		require.NoError(t, service.ApplyMicrosoftFetchRefreshToken(txCtx, coreapp.MicrosoftFetchRefreshTokenRotation{
			ResourceID: 8101, ExpectedCredentialRevision: 3,
			RefreshToken: "refresh-must-roll-back", Now: time.Now().UTC(),
		}))
		return forcedRollback
	})
	require.ErrorIs(t, err, forcedRollback)

	var rolledBack struct {
		RefreshToken       string `gorm:"column:refresh_token"`
		CredentialRevision uint64 `gorm:"column:credential_revision"`
		Version            uint64 `gorm:"column:version"`
	}
	require.NoError(t, db.Raw(`
SELECT mr.refresh_token, mr.credential_revision, er.version
FROM microsoft_resources mr
JOIN email_resources er ON er.id = mr.id
WHERE mr.id = ?`, 8101).Scan(&rolledBack).Error)
	require.Equal(t, "refresh-v1", rolledBack.RefreshToken)
	require.Equal(t, uint64(3), rolledBack.CredentialRevision)
	require.Equal(t, uint64(1), rolledBack.Version)

	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, service.ApplyMicrosoftTokenRefreshSuccess(context.Background(), coreapp.MicrosoftTokenRefreshSuccess{
		ResourceID: 8101, ExpectedCredentialRevision: 3,
		ClientID: "client-v2", RefreshToken: "refresh-v2", RequestID: "request-8101", Now: now,
	}))

	var saved struct {
		ClientID             string     `gorm:"column:client_id"`
		RefreshToken         string     `gorm:"column:refresh_token"`
		CredentialRevision   uint64     `gorm:"column:credential_revision"`
		TokenLastRefreshedAt *time.Time `gorm:"column:token_last_refreshed_at"`
		TokenLastRequestID   string     `gorm:"column:token_last_request_id"`
		Status               string     `gorm:"column:status"`
		Version              uint64     `gorm:"column:version"`
	}
	require.NoError(t, db.Raw(`
SELECT mr.client_id, mr.refresh_token, mr.credential_revision,
       mr.token_last_refreshed_at, mr.token_last_request_id, mr.status, er.version
FROM microsoft_resources mr
JOIN email_resources er ON er.id = mr.id
WHERE mr.id = ?`, 8101).Scan(&saved).Error)
	require.Equal(t, "client-v2", saved.ClientID)
	require.Equal(t, "refresh-v2", saved.RefreshToken)
	require.Equal(t, uint64(4), saved.CredentialRevision)
	require.NotNil(t, saved.TokenLastRefreshedAt)
	require.Equal(t, now, saved.TokenLastRefreshedAt.UTC())
	require.Equal(t, "request-8101", saved.TokenLastRequestID)
	require.Equal(t, "disabled", saved.Status)
	require.Equal(t, uint64(2), saved.Version)
}
