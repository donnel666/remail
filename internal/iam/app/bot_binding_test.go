package app_test

import (
	"context"
	"testing"

	iamapp "github.com/donnel666/remail/internal/iam/app"
	"github.com/donnel666/remail/internal/iam/domain"
	iaminfra "github.com/donnel666/remail/internal/iam/infra"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type botBindingHasher struct{}

func (botBindingHasher) Hash(password string) (string, error) { return password, nil }
func (botBindingHasher) Verify(password, hash string) bool    { return password == hash }

func botBindingFixture(t *testing.T) (*gorm.DB, *iaminfra.UserRepo, *iamapp.BotBindingUseCase) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&iaminfra.UserGroupModel{}, &iaminfra.UserModel{}, &iaminfra.ThirdPartyIdentityModel{}))
	require.NoError(t, db.Create(&iaminfra.UserGroupModel{
		ID: 1, Code: "default", Name: "Default", Enabled: true,
		APIConcurrencyLimit: 1, PriceDiscountRatio: "1.000000", TopupThreshold: "0.000000",
	}).Error)
	for _, user := range []iaminfra.UserModel{
		{ID: 1, Email: "alice@example.com", PasswordHash: "alice-password", Status: string(domain.UserStatusActive), Role: string(domain.RoleUser), UserGroupID: 1},
		{ID: 2, Email: "bob@example.com", PasswordHash: "bob-password", Status: string(domain.UserStatusActive), Role: string(domain.RoleUser), UserGroupID: 1},
	} {
		require.NoError(t, db.Create(&user).Error)
	}
	repo := iaminfra.NewUserRepo(db)
	return db, repo, iamapp.NewBotBindingUseCase(repo, botBindingHasher{})
}

func TestBotBindingUsesCredentialSnapshotAndOneIdentityPerPlatformNamespace(t *testing.T) {
	db, repo, bindings := botBindingFixture(t)
	ctx := context.Background()
	_, err := bindings.Bind(ctx, "qq_official", "qq:main", "openid-alice", "alice@example.com", "alice-password")
	require.ErrorIs(t, err, domain.ErrAccountOrPasswordIncorrect)

	info, err := bindings.Bind(ctx, "aiocqhttp", "qq:main", "123456789", " Alice@Example.com ", "alice-password")
	require.NoError(t, err)
	require.True(t, info.Bound)
	require.Equal(t, "a***@example.com", info.MaskedEmail)
	var rawSubjectCount int64
	require.NoError(t, db.Model(&iaminfra.ThirdPartyIdentityModel{}).
		Where("provider_user_id = ?", "123456789").Count(&rawSubjectCount).Error)
	require.EqualValues(t, 1, rawSubjectCount)

	userID, ok, err := bindings.ResolveActiveUserID(ctx, "aiocqhttp", "qq:main", "123456789")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint(1), userID)
	resolved, ok, err := bindings.ResolveActiveUser(ctx, "aiocqhttp", "qq:main", "123456789")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "1", resolved.PriceDiscountRatio)

	_, err = bindings.Bind(ctx, "aiocqhttp", "qq:main", "987654321", "alice@example.com", "alice-password")
	require.ErrorIs(t, err, domain.ErrThirdPartyIdentityAlreadyBound)
	_, err = bindings.Bind(ctx, "aiocqhttp", "qq:main", "123456789", "bob@example.com", "bob-password")
	require.ErrorIs(t, err, domain.ErrThirdPartyIdentityAlreadyBound)

	// The same account may bind another independently scoped Bot integration.
	_, err = bindings.Bind(ctx, "telegram", "qq:main", "123456789", "alice@example.com", "alice-password")
	require.NoError(t, err)

	// A password change between verification and commit rejects the stale
	// snapshot and leaves no new identity behind.
	require.ErrorIs(t,
		repo.BindThirdPartyIdentityWithPasswordSnapshot(ctx, 2, "stale-hash", "telegram:main", "456"),
		domain.ErrAccountOrPasswordIncorrect,
	)
	var staleCount int64
	require.NoError(t, db.Model(&iaminfra.ThirdPartyIdentityModel{}).
		Where("provider = ? AND provider_user_id = ?", "telegram:main", "456").Count(&staleCount).Error)
	require.Zero(t, staleCount)
}

func TestBotBindingStatusIsSafeAndUnbindIsIdempotent(t *testing.T) {
	db, _, bindings := botBindingFixture(t)
	ctx := context.Background()

	_, err := bindings.Bind(ctx, "aiocqhttp", "qq:main", "123456789", "alice@example.com", "wrong")
	require.ErrorIs(t, err, domain.ErrAccountOrPasswordIncorrect)
	_, err = bindings.Bind(ctx, "aiocqhttp", "qq:main", "123456789", "alice@example.com", "alice-password")
	require.NoError(t, err)

	require.NoError(t, db.Model(&iaminfra.UserModel{}).Where("id = ?", 1).Update("status", domain.UserStatusDisabled).Error)
	info, err := bindings.Get(ctx, "aiocqhttp", "qq:main", "123456789")
	require.NoError(t, err)
	require.True(t, info.Bound)
	require.False(t, info.Available)
	require.Empty(t, info.MaskedEmail)

	require.NoError(t, bindings.Unbind(ctx, "aiocqhttp", "qq:main", "123456789"))
	require.NoError(t, bindings.Unbind(ctx, "aiocqhttp", "qq:main", "123456789"))
	info, err = bindings.Get(ctx, "aiocqhttp", "qq:main", "123456789")
	require.NoError(t, err)
	require.False(t, info.Bound)
}
