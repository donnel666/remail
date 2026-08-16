package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	settingsapp "github.com/donnel666/remail/internal/systemsettings/app"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	settingsinfra "github.com/donnel666/remail/internal/systemsettings/infra"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSystemKeyPlaintextIsReturnedOnceAndRevocationStopsAuthentication(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:system-key?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&settingsinfra.SystemKeyModel{}, &governanceinfra.OperationLogModel{}))

	repo := settingsinfra.NewRepository(db)
	useCase := settingsapp.NewSystemKeyUseCase(repo, governanceinfra.NewOperationLogRepo(db))
	ctx := context.Background()
	created, err := useCase.Create(ctx, "iCloud worker", settingsapp.MutationMeta{OperatorUserID: 7})
	require.NoError(t, err)
	require.NotEmpty(t, created.KeyPlain)
	require.True(t, strings.HasPrefix(created.KeyPlain, "sk_"))
	require.Len(t, created.KeyPlain, 46)

	var stored settingsinfra.SystemKeyModel
	require.NoError(t, db.First(&stored, created.ID).Error)
	require.NotEqual(t, created.KeyPlain, stored.KeyHash)
	require.Len(t, stored.KeyHash, 64)

	listed, err := useCase.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Empty(t, listed[0].KeyPlain)

	keyID, err := useCase.AuthenticateSystemKey(ctx, created.KeyPlain)
	require.NoError(t, err)
	require.Equal(t, created.ID, keyID)
	var usedOnce settingsinfra.SystemKeyModel
	require.NoError(t, db.First(&usedOnce, created.ID).Error)
	require.NotNil(t, usedOnce.LastUsedAt)

	keyID, err = useCase.AuthenticateSystemKey(ctx, created.KeyPlain)
	require.NoError(t, err)
	require.Equal(t, created.ID, keyID)
	var usedTwice settingsinfra.SystemKeyModel
	require.NoError(t, db.First(&usedTwice, created.ID).Error)
	require.Equal(t, usedOnce.LastUsedAt, usedTwice.LastUsedAt)

	require.NoError(t, useCase.Delete(ctx, created.ID, settingsapp.MutationMeta{OperatorUserID: 7}))
	_, err = useCase.AuthenticateSystemKey(ctx, created.KeyPlain)
	require.True(t, errors.Is(err, settingsdomain.ErrSystemKeyNotFound))
}

func TestSystemKeyAuthenticationRejectsMalformedCredentials(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:system-key-format?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&settingsinfra.SystemKeyModel{}, &governanceinfra.OperationLogModel{}))

	useCase := settingsapp.NewSystemKeyUseCase(
		settingsinfra.NewRepository(db),
		governanceinfra.NewOperationLogRepo(db),
	)
	for _, plain := range []string{
		"",
		"sk_",
		"sk_short",
		"sk_!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!",
		"api_0123456789012345678901234567890123456789012",
	} {
		_, err := useCase.AuthenticateSystemKey(context.Background(), plain)
		require.ErrorIs(t, err, settingsdomain.ErrInvalidSystemKey, plain)
	}
}
