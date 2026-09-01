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
	created, err := useCase.Create(ctx, "iCloud worker", settingsdomain.SystemKeyPurposeICloudForwarding, settingsapp.MutationMeta{OperatorUserID: 7})
	require.NoError(t, err)
	require.NotEmpty(t, created.KeyPlain)
	require.True(t, strings.HasPrefix(created.KeyPlain, "sk_"))
	require.Len(t, created.KeyPlain, 46)

	var stored settingsinfra.SystemKeyModel
	require.NoError(t, db.First(&stored, created.ID).Error)
	require.NotEqual(t, created.KeyPlain, stored.KeyHash)
	require.Len(t, stored.KeyHash, 64)
	require.Equal(t, string(settingsdomain.SystemKeyPurposeICloudForwarding), stored.Purpose)

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
	_, err = useCase.AuthenticateSMTPSubmissionKey(ctx, created.KeyPlain)
	require.ErrorIs(t, err, settingsdomain.ErrInvalidSystemKey)

	smtpKey, err := useCase.Create(ctx, "SMTP sender", settingsdomain.SystemKeyPurposeSMTPSubmission, settingsapp.MutationMeta{OperatorUserID: 7})
	require.NoError(t, err)
	_, err = useCase.AuthenticateSMTPSubmissionKey(ctx, smtpKey.KeyPlain)
	require.NoError(t, err)
	_, err = useCase.AuthenticateSystemKey(ctx, smtpKey.KeyPlain)
	require.ErrorIs(t, err, settingsdomain.ErrInvalidSystemKey)

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

func TestBotSystemKeyRequiresAndReturnsStableScope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:bot-system-key?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&settingsinfra.SystemKeyModel{}, &governanceinfra.OperationLogModel{}))
	useCase := settingsapp.NewSystemKeyUseCase(settingsinfra.NewRepository(db), governanceinfra.NewOperationLogRepo(db))
	meta := settingsapp.MutationMeta{OperatorUserID: 7}

	for _, scope := range [][2]string{
		{"", "qq:main"}, {"qq", ""}, {"QQ 官方", "qq:main"},
		{"aiocqhttp", "qq:main"}, {"qq", "telegram:main"}, {"telegram", "qq:main"},
		{"qq", "qq:corp"}, {"telegram", "telegram:bot1"},
	} {
		_, err := useCase.CreateWithScope(context.Background(), "bad bot", settingsdomain.SystemKeyPurposeBot, scope[0], scope[1], meta)
		require.ErrorIs(t, err, settingsdomain.ErrInvalidSystemKey)
	}
	_, err = useCase.CreateWithScope(context.Background(), "bad smtp", settingsdomain.SystemKeyPurposeSMTPSubmission, "telegram", "telegram:main", meta)
	require.ErrorIs(t, err, settingsdomain.ErrInvalidSystemKey)
	for _, groupID := range []string{"telegram-group", "0", "--1001"} {
		_, err = useCase.CreateWithScope(
			context.Background(), "bad Telegram group", settingsdomain.SystemKeyPurposeBot,
			"telegram", "telegram:main", meta, groupID,
		)
		require.ErrorIs(t, err, settingsdomain.ErrInvalidSystemKey, groupID)
	}

	created, err := useCase.CreateWithScope(
		context.Background(), "QQ main", settingsdomain.SystemKeyPurposeBot,
		" QQ ", " QQ:MAIN ", meta, " 456 ", "123", "123",
	)
	require.NoError(t, err)
	require.Equal(t, "qq", created.Platform)
	require.Equal(t, "qq:main", created.SubjectNamespace)
	require.Equal(t, []string{"123", "456"}, created.AllowedGroupIDs)

	authenticated, err := useCase.AuthenticateBotSystemKey(context.Background(), created.KeyPlain)
	require.NoError(t, err)
	require.Equal(t, created.ID, authenticated.ID)
	require.Equal(t, "qq", authenticated.Platform)
	require.Equal(t, "qq:main", authenticated.SubjectNamespace)
	require.Equal(t, []string{"123", "456"}, authenticated.AllowedGroupIDs)
	require.Empty(t, authenticated.KeyPlain)
	telegram, err := useCase.CreateWithScope(
		context.Background(), "Telegram main", settingsdomain.SystemKeyPurposeBot,
		"telegram", "telegram:main", meta, "-1001234567890",
	)
	require.NoError(t, err)
	require.Equal(t, "telegram", telegram.Platform)
	require.Equal(t, []string{"-1001234567890"}, telegram.AllowedGroupIDs)

	_, err = useCase.AuthenticateSystemKey(context.Background(), created.KeyPlain)
	require.ErrorIs(t, err, settingsdomain.ErrInvalidSystemKey)
}
