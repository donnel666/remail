package infra

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecordMicrosoftFetchFailurePersistsRotatedToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:resource-fetch-failure?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&MicrosoftResourceModel{}))
	require.NoError(t, db.Create(&MicrosoftResourceModel{
		ID: 95, EmailAddress: "owner@example.test", RefreshToken: "refresh-v1",
		CredentialRevision: 7, CredentialUpdatedAt: time.Now().UTC(), Status: string(domain.MicrosoftStatusNormal),
		GraphAvailable: true, QualityScore: 100,
	}).Error)

	abnormal, err := (&ResourceValidationRepo{db: db}).RecordMicrosoftFetchFailure(
		context.Background(), 95, 7, "refresh-v2", "Microsoft refresh token is invalid or expired.", "request-95", nil,
	)

	require.NoError(t, err)
	require.True(t, abnormal)
	var stored MicrosoftResourceModel
	require.NoError(t, db.First(&stored, 95).Error)
	require.Equal(t, string(domain.MicrosoftStatusAbnormal), stored.Status)
	require.Equal(t, "refresh-v2", stored.RefreshToken)
	require.Equal(t, uint64(8), stored.CredentialRevision)
	require.Equal(t, "request-95", stored.TokenLastRequestID)
	require.NotNil(t, stored.TokenLastRefreshedAt)
	require.False(t, stored.GraphAvailable)
	require.Zero(t, stored.QualityScore)
}
