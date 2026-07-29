package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type linuxDOLoginRepoStub struct {
	UserRepository
	users         map[uint]*domain.User
	byEmail       map[string]uint
	linuxDOID     string
	linuxDOUserID uint
	creates       int
	updates       int
}

func newLinuxDOLoginRepoStub(users ...*domain.User) *linuxDOLoginRepoStub {
	r := &linuxDOLoginRepoStub{users: make(map[uint]*domain.User), byEmail: make(map[string]uint)}
	for _, user := range users {
		cp := *user
		r.users[user.ID] = &cp
		r.byEmail[normalizeEmail(user.Email)] = user.ID
	}
	return r
}

func (r *linuxDOLoginRepoStub) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	user := r.users[r.byEmail[normalizeEmail(email)]]
	if user == nil {
		return nil, nil
	}
	cp := *user
	return &cp, nil
}

func (r *linuxDOLoginRepoStub) FindByLinuxDOID(_ context.Context, linuxDOID string) (*domain.User, error) {
	if r.linuxDOID != linuxDOID || r.linuxDOUserID == 0 {
		return nil, nil
	}
	user := r.users[r.linuxDOUserID]
	if user == nil {
		return nil, nil
	}
	cp := *user
	return &cp, nil
}

func (r *linuxDOLoginRepoStub) CreateWithLinuxDOIdentity(_ context.Context, user *domain.User, linuxDOID string) error {
	if _, exists := r.byEmail[normalizeEmail(user.Email)]; exists {
		return domain.ErrEmailAlreadyExists
	}
	if r.linuxDOUserID != 0 {
		return domain.ErrLinuxDOIdentityAlreadyBound
	}
	r.creates++
	user.ID = uint(len(r.users) + 40)
	cp := *user
	r.users[user.ID] = &cp
	r.byEmail[normalizeEmail(user.Email)] = user.ID
	r.linuxDOID = linuxDOID
	r.linuxDOUserID = user.ID
	return nil
}

func (r *linuxDOLoginRepoStub) BindLinuxDOIdentity(_ context.Context, userID uint, linuxDOID string) error {
	if r.linuxDOUserID != 0 && r.linuxDOUserID != userID {
		return domain.ErrLinuxDOIdentityAlreadyBound
	}
	r.linuxDOID = linuxDOID
	r.linuxDOUserID = userID
	return nil
}

func (r *linuxDOLoginRepoStub) UpdateLinuxDOPlaceholder(_ context.Context, legacyUserID uint, linuxDOID, email, passwordHash string) error {
	user := r.users[legacyUserID]
	if user == nil || r.linuxDOUserID != legacyUserID || r.linuxDOID != linuxDOID {
		return domain.ErrLinuxDOAccountUnavailable
	}
	if _, exists := r.byEmail[normalizeEmail(email)]; exists {
		return domain.ErrEmailAlreadyExists
	}
	delete(r.byEmail, normalizeEmail(user.Email))
	user.Email = normalizeEmail(email)
	user.PasswordHash = passwordHash
	r.byEmail[user.Email] = user.ID
	r.updates++
	return nil
}

func (r *linuxDOLoginRepoStub) RecordLinuxDOLogin(_ context.Context, userID uint, linuxDOID string) (*domain.User, error) {
	if r.linuxDOUserID != userID || r.linuxDOID != linuxDOID {
		return nil, nil
	}
	user := r.users[userID]
	if user == nil || !user.IsActive() {
		return nil, nil
	}
	cp := *user
	return &cp, nil
}

func configureLinuxDOAppTest(t *testing.T) {
	t.Helper()
	runtimeconfig.Set("register_enabled", "true")
	runtimeconfig.Set("registration_email_whitelist", "qq.com")
	runtimeconfig.Set("linuxdo_minimum_trust_level", "0")
	runtimeconfig.Set("registration_reward_amount", "5")
	t.Cleanup(func() {
		runtimeconfig.Delete("register_enabled")
		runtimeconfig.Delete("registration_email_whitelist")
		runtimeconfig.Delete("linuxdo_minimum_trust_level")
		runtimeconfig.Delete("registration_reward_amount")
	})
}

func newLinuxDOLoginUseCase(repo UserRepository, codeStore EmailCodeStore, sessions *credentialSessionStoreStub) *LoginUseCase {
	uc := NewLoginUseCase(repo, registrationHasherStub{}, sessions)
	uc.SetEmailCodeStore(codeStore)
	return uc
}

func TestLinuxDOFirstLoginRequiresVerifiedAccountOwnership(t *testing.T) {
	configureLinuxDOAppTest(t)
	repo := newLinuxDOLoginRepoStub()
	sessions := &credentialSessionStoreStub{}
	result, pending, err := newLinuxDOLoginUseCase(repo, newEmailCodeStoreStub(), sessions).
		LoginLinuxDO(context.Background(), LinuxDOProfile{ID: "42", Username: "linuxdo-user", Email: "member@outside.test", Active: true})

	require.NoError(t, err)
	require.Nil(t, result)
	require.NotNil(t, pending)
	require.Equal(t, "member@outside.test", pending.SuggestedEmail)
	require.False(t, pending.SuggestedEmailExists)
	require.Zero(t, repo.creates)
	require.Nil(t, sessions.created)
}

func TestLinuxDOExistingBindingLogsInDirectly(t *testing.T) {
	configureLinuxDOAppTest(t)
	user := &domain.User{ID: 7, Email: "user@qq.com", PasswordHash: "$2a$local", Status: domain.UserStatusActive, Role: domain.RoleUser}
	repo := newLinuxDOLoginRepoStub(user)
	repo.linuxDOID, repo.linuxDOUserID = "42", user.ID
	sessions := &credentialSessionStoreStub{}

	result, pending, err := newLinuxDOLoginUseCase(repo, newEmailCodeStoreStub(), sessions).
		LoginLinuxDO(context.Background(), LinuxDOProfile{ID: "42", Active: true})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, pending)
	require.EqualValues(t, user.ID, sessions.created.UserID)
}

func TestLinuxDOCreateNewAccountUsesVerifiedEmailAndNoLocalPassword(t *testing.T) {
	configureLinuxDOAppTest(t)
	repo := newLinuxDOLoginRepoStub()
	store := newEmailCodeStoreStub()
	store.codes[linuxDOEmailCodeKey("newuser@qq.com")] = "123456"
	wallet := &registrationRewardWalletStub{}
	uc := newLinuxDOLoginUseCase(repo, store, &credentialSessionStoreStub{})
	uc.SetRegistrationRewardWallet(wallet)
	pending := LinuxDOPending{Profile: LinuxDOProfile{ID: "42", Name: "LinuxDo User", Active: true}}

	result, err := uc.CompleteLinuxDO(context.Background(), pending, LinuxDOAccountNew, "newuser@qq.com", "123456")

	require.NoError(t, err)
	require.Equal(t, "newuser@qq.com", result.User.Email)
	require.True(t, strings.HasPrefix(result.User.PasswordHash, "!oauth:"))
	require.False(t, result.User.HasLocalPassword())
	require.Equal(t, 1, repo.creates)
	require.Equal(t, 1, wallet.calls)
}

func TestLinuxDOProviderEmailCanBypassRegistrationDomainWhitelistAfterVerification(t *testing.T) {
	configureLinuxDOAppTest(t)
	repo := newLinuxDOLoginRepoStub()
	store := newEmailCodeStoreStub()
	store.codes[linuxDOEmailCodeKey("first.last@outside.test")] = "123456"
	uc := newLinuxDOLoginUseCase(repo, store, &credentialSessionStoreStub{})
	pending := LinuxDOPending{Profile: LinuxDOProfile{ID: "42", Email: "First.Last@Outside.Test", Active: true}}

	result, err := uc.CompleteLinuxDO(context.Background(), pending, LinuxDOAccountNew, "first.last@outside.test", "123456")

	require.NoError(t, err)
	require.Equal(t, "first.last@outside.test", result.User.Email)
}

func TestLinuxDOInvalidProviderEmailCannotBypassRegistrationWhitelist(t *testing.T) {
	configureLinuxDOAppTest(t)
	uc := newLinuxDOLoginUseCase(newLinuxDOLoginRepoStub(), newEmailCodeStoreStub(), &credentialSessionStoreStub{})
	profile := LinuxDOProfile{ID: "42", Email: "linuxdo-96729@oauth.invalid", Active: true}

	_, pending, err := uc.LoginLinuxDO(context.Background(), profile)
	require.NoError(t, err)
	require.Empty(t, pending.SuggestedEmail)

	_, err = uc.CompleteLinuxDO(context.Background(), *pending, LinuxDOAccountNew, profile.Email, "123456")
	require.Error(t, err)
}

func TestLinuxDOManualNewEmailStillUsesRegistrationWhitelist(t *testing.T) {
	configureLinuxDOAppTest(t)
	uc := newLinuxDOLoginUseCase(newLinuxDOLoginRepoStub(), newEmailCodeStoreStub(), &credentialSessionStoreStub{})
	pending := LinuxDOPending{Profile: LinuxDOProfile{ID: "42", Email: "provider@outside.test", Active: true}}

	_, err := uc.CompleteLinuxDO(context.Background(), pending, LinuxDOAccountNew, "other@outside.test", "123456")

	require.ErrorIs(t, err, domain.ErrRegistrationEmailDomainBlocked)
}

func TestLinuxDOBindExistingAccountVerifiesEmailWithoutChangingCredentials(t *testing.T) {
	configureLinuxDOAppTest(t)
	user := &domain.User{ID: 7, Email: "owner@qq.com", PasswordHash: "$2a$unchanged", Status: domain.UserStatusActive, Role: domain.RoleUser}
	repo := newLinuxDOLoginRepoStub(user)
	store := newEmailCodeStoreStub()
	store.codes[linuxDOEmailCodeKey("owner@qq.com")] = "123456"
	pending := LinuxDOPending{Profile: LinuxDOProfile{ID: "42", Email: "owner@qq.com", Active: true}}

	result, err := newLinuxDOLoginUseCase(repo, store, &credentialSessionStoreStub{}).
		CompleteLinuxDO(context.Background(), pending, LinuxDOAccountExisting, "owner@qq.com", "123456")

	require.NoError(t, err)
	require.EqualValues(t, user.ID, result.User.ID)
	require.Equal(t, "$2a$unchanged", result.User.PasswordHash)
	require.EqualValues(t, user.ID, repo.linuxDOUserID)
}

func TestLinuxDORegistrationDisabledStillAllowsVerifiedExistingAccount(t *testing.T) {
	configureLinuxDOAppTest(t)
	runtimeconfig.Set("register_enabled", "false")
	store := newEmailCodeStoreStub()
	delivery := &mailDeliveryStub{}
	emailCodes := NewEmailCodeUseCase(store, delivery)

	_, err := emailCodes.RequestLinuxDO(context.Background(), "new@qq.com", "new@qq.com", LinuxDOAccountNew, false)
	require.ErrorIs(t, err, domain.ErrRegistrationDisabled)

	created, err := emailCodes.RequestLinuxDO(context.Background(), "owner@outside.test", "", LinuxDOAccountExisting, false)
	require.NoError(t, err)
	require.True(t, created)
	code, err := store.Get(context.Background(), linuxDOEmailCodeKey("owner@outside.test"))
	require.NoError(t, err)

	user := &domain.User{ID: 7, Email: "owner@outside.test", PasswordHash: "$2a$unchanged", Status: domain.UserStatusActive, Role: domain.RoleUser}
	result, err := newLinuxDOLoginUseCase(newLinuxDOLoginRepoStub(user), store, &credentialSessionStoreStub{}).
		CompleteLinuxDO(context.Background(), LinuxDOPending{Profile: LinuxDOProfile{ID: "42", Active: true}}, LinuxDOAccountExisting, user.Email, code)
	require.NoError(t, err)
	require.EqualValues(t, user.ID, result.User.ID)
}

func TestLinuxDOLegacyPlaceholderIsUpgradedInPlace(t *testing.T) {
	configureLinuxDOAppTest(t)
	runtimeconfig.Set("register_enabled", "false")
	legacy := &domain.User{ID: 9, Email: "linuxdo-42@oauth.invalid", PasswordHash: "$2a$legacy", Status: domain.UserStatusActive, Role: domain.RoleUser}
	repo := newLinuxDOLoginRepoStub(legacy)
	repo.linuxDOID, repo.linuxDOUserID = "42", legacy.ID
	store := newEmailCodeStoreStub()
	created, err := NewEmailCodeUseCase(store, &mailDeliveryStub{}).
		RequestLinuxDO(context.Background(), "owner@qq.com", "", LinuxDOAccountNew, true)
	require.NoError(t, err)
	require.True(t, created)
	code, err := store.Get(context.Background(), linuxDOEmailCodeKey("owner@qq.com"))
	require.NoError(t, err)
	wallet := &registrationRewardWalletStub{}
	uc := newLinuxDOLoginUseCase(repo, store, &credentialSessionStoreStub{})
	uc.SetRegistrationRewardWallet(wallet)

	_, pending, err := uc.LoginLinuxDO(context.Background(), LinuxDOProfile{ID: "42", Active: true})
	require.NoError(t, err)
	require.EqualValues(t, legacy.ID, pending.LegacyUserID)
	result, err := uc.CompleteLinuxDO(context.Background(), *pending, LinuxDOAccountNew, "owner@qq.com", code)

	require.NoError(t, err)
	require.EqualValues(t, legacy.ID, result.User.ID)
	require.Equal(t, "owner@qq.com", result.User.Email)
	require.Equal(t, 1, repo.updates)
	require.Zero(t, wallet.calls)
}

func TestLinuxDOLegacyPlaceholderCannotMergeIntoExistingAccount(t *testing.T) {
	configureLinuxDOAppTest(t)
	legacy := &domain.User{ID: 9, Email: "linuxdo-42@oauth.invalid", PasswordHash: "$2a$legacy", Status: domain.UserStatusActive, Role: domain.RoleUser}
	existing := &domain.User{ID: 7, Email: "owner@qq.com", PasswordHash: "$2a$existing", Status: domain.UserStatusActive, Role: domain.RoleUser}
	repo := newLinuxDOLoginRepoStub(legacy, existing)
	repo.linuxDOID, repo.linuxDOUserID = "42", legacy.ID
	store := newEmailCodeStoreStub()
	store.codes[linuxDOEmailCodeKey(existing.Email)] = "123456"

	_, err := newLinuxDOLoginUseCase(repo, store, &credentialSessionStoreStub{}).
		CompleteLinuxDO(context.Background(), LinuxDOPending{
			Profile:      LinuxDOProfile{ID: "42", Active: true},
			LegacyUserID: legacy.ID,
		}, LinuxDOAccountExisting, existing.Email, "123456")

	require.ErrorIs(t, err, domain.ErrLinuxDOLegacyMergeUnsupported)
	bound, findErr := repo.FindByLinuxDOID(context.Background(), "42")
	require.NoError(t, findErr)
	require.EqualValues(t, legacy.ID, bound.ID)
	require.Equal(t, legacy.Email, bound.Email)
	require.Zero(t, repo.updates)
	code, getErr := store.Get(context.Background(), linuxDOEmailCodeKey(existing.Email))
	require.NoError(t, getErr)
	require.Equal(t, "123456", code)
}

func TestLinuxDONewAccountCompletionCanRetryAfterSessionFailure(t *testing.T) {
	configureLinuxDOAppTest(t)
	repo := newLinuxDOLoginRepoStub()
	store := newEmailCodeStoreStub()
	store.codes[linuxDOEmailCodeKey("newuser@qq.com")] = "123456"
	sessions := &credentialSessionStoreStub{createErr: errors.New("redis unavailable")}
	wallet := &registrationRewardWalletStub{}
	uc := newLinuxDOLoginUseCase(repo, store, sessions)
	uc.SetRegistrationRewardWallet(wallet)
	pending := LinuxDOPending{Profile: LinuxDOProfile{ID: "42", Active: true}}

	_, err := uc.CompleteLinuxDO(context.Background(), pending, LinuxDOAccountNew, "newuser@qq.com", "123456")
	require.ErrorContains(t, err, "redis unavailable")
	require.Equal(t, 1, repo.creates)
	require.Equal(t, 1, wallet.calls)
	require.Nil(t, sessions.created)
	code, getErr := store.Get(context.Background(), linuxDOEmailCodeKey("newuser@qq.com"))
	require.NoError(t, getErr)
	require.Equal(t, "123456", code)

	sessions.createErr = nil
	result, err := uc.CompleteLinuxDO(context.Background(), pending, LinuxDOAccountNew, "newuser@qq.com", "123456")

	require.NoError(t, err)
	require.Equal(t, "newuser@qq.com", result.User.Email)
	require.Equal(t, 1, repo.creates)
	require.Equal(t, 1, wallet.calls)
	require.EqualValues(t, result.User.ID, sessions.created.UserID)
}

func TestLinuxDOSilencedAccountIsRejected(t *testing.T) {
	_, err := normalizeLinuxDOProfile(LinuxDOProfile{ID: "42", Active: true, Silenced: true})
	require.ErrorIs(t, err, domain.ErrLinuxDOAccountUnavailable)
}
