package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type githubLoginRepoStub struct {
	UserRepository
	users          map[uint]*domain.User
	byEmail        map[string]uint
	byIdentity     map[string]uint
	createConflict error
	conflictBinds  bool
	binds          int
}

func newGitHubLoginRepoStub(users ...*domain.User) *githubLoginRepoStub {
	r := &githubLoginRepoStub{
		users:      make(map[uint]*domain.User),
		byEmail:    make(map[string]uint),
		byIdentity: make(map[string]uint),
	}
	for _, user := range users {
		r.store(user)
	}
	return r
}

func (r *githubLoginRepoStub) store(user *domain.User) *domain.User {
	cp := *user
	if cp.ID == 0 {
		cp.ID = uint(len(r.users) + 40)
	}
	r.users[cp.ID] = &cp
	r.byEmail[normalizeEmail(cp.Email)] = cp.ID
	return &cp
}

func (r *githubLoginRepoStub) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	return r.find(r.byEmail[normalizeEmail(email)]), nil
}

func (r *githubLoginRepoStub) FindByID(_ context.Context, userID uint) (*domain.User, error) {
	return r.find(userID), nil
}

func (r *githubLoginRepoStub) FindByThirdPartyIdentity(_ context.Context, provider, providerUserID string) (*domain.User, error) {
	if provider != githubIdentityProvider {
		return nil, nil
	}
	return r.find(r.byIdentity[providerUserID]), nil
}

func (r *githubLoginRepoStub) CreateWithThirdPartyIdentity(_ context.Context, user *domain.User, provider, providerUserID string) error {
	if r.createConflict != nil {
		conflict := r.createConflict
		r.createConflict = nil
		concurrent := r.store(&domain.User{
			ID:           77,
			Email:        user.Email,
			PasswordHash: "!oauth:concurrent",
			Nickname:     user.Nickname,
			Status:       domain.UserStatusActive,
			Role:         domain.RoleUser,
		})
		if r.conflictBinds {
			r.byIdentity[providerUserID] = concurrent.ID
		}
		return conflict
	}
	stored := r.store(user)
	user.ID = stored.ID
	r.byIdentity[providerUserID] = stored.ID
	return nil
}

func (r *githubLoginRepoStub) BindThirdPartyIdentity(_ context.Context, userID uint, provider, providerUserID string) error {
	if provider != githubIdentityProvider || r.users[userID] == nil {
		return domain.ErrThirdPartyIdentityUnavailable
	}
	if owner := r.byIdentity[providerUserID]; owner != 0 && owner != userID {
		return domain.ErrThirdPartyIdentityAlreadyBound
	}
	r.byIdentity[providerUserID] = userID
	r.binds++
	return nil
}

func (r *githubLoginRepoStub) RecordThirdPartyLogin(_ context.Context, userID uint, provider, providerUserID string) (*domain.User, error) {
	if provider != githubIdentityProvider || r.byIdentity[providerUserID] != userID {
		return nil, nil
	}
	return r.find(userID), nil
}

func (r *githubLoginRepoStub) find(userID uint) *domain.User {
	user := r.users[userID]
	if user == nil {
		return nil
	}
	cp := *user
	return &cp
}

func configureGitHubAppTest(t *testing.T) {
	t.Helper()
	runtimeconfig.Set("register_enabled", "true")
	runtimeconfig.Set("github_minimum_account_age_days", "365")
	t.Cleanup(func() {
		runtimeconfig.Delete("register_enabled")
		runtimeconfig.Delete("github_minimum_account_age_days")
	})
}

func githubAppProfile() GitHubProfile {
	return GitHubProfile{
		ID:        "42",
		Username:  "octocat",
		Email:     "owner@example.com",
		CreatedAt: time.Now().AddDate(-2, 0, 0),
	}
}

func TestGitHubExistingEmailRequiresCurrentMailboxProof(t *testing.T) {
	configureGitHubAppTest(t)
	existing := &domain.User{ID: 7, Email: "owner@example.com", PasswordHash: "$2a$unchanged", Status: domain.UserStatusActive, Role: domain.RoleAdmin}
	repo := newGitHubLoginRepoStub(existing)
	sessions := &credentialSessionStoreStub{}
	uc := NewLoginUseCase(repo, registrationHasherStub{}, sessions)
	uc.SetEmailCodeStore(newEmailCodeStoreStub())

	result, pending, err := uc.LoginGitHub(context.Background(), githubAppProfile())

	require.NoError(t, err)
	require.Nil(t, result)
	require.EqualValues(t, existing.ID, pending.UserID)
	require.Equal(t, existing.Email, pending.Email)
	require.Zero(t, repo.binds)
	require.Nil(t, sessions.created)
}

func TestGitHubVerifiedCodeBindsExistingAccountWithoutChangingPrivileges(t *testing.T) {
	configureGitHubAppTest(t)
	existing := &domain.User{ID: 7, Email: "owner@example.com", PasswordHash: "$2a$unchanged", Status: domain.UserStatusActive, Role: domain.RoleAdmin}
	repo := newGitHubLoginRepoStub(existing)
	store := newEmailCodeStoreStub()
	store.codes[githubEmailCodeKey(existing.Email)] = "123456"
	sessions := &credentialSessionStoreStub{}
	uc := NewLoginUseCase(repo, registrationHasherStub{}, sessions)
	uc.SetEmailCodeStore(store)
	pending := GitHubPending{Profile: githubAppProfile(), Intent: "login", UserID: existing.ID, Email: existing.Email}

	result, err := uc.CompleteGitHub(context.Background(), pending, "123456")

	require.NoError(t, err)
	require.Equal(t, domain.RoleAdmin, result.User.Role)
	require.Equal(t, "$2a$unchanged", result.User.PasswordHash)
	require.EqualValues(t, existing.ID, sessions.created.UserID)
	require.Equal(t, 1, repo.binds)
}

func TestGitHubConcurrentFirstLoginRecoversCommittedIdentity(t *testing.T) {
	configureGitHubAppTest(t)
	repo := newGitHubLoginRepoStub()
	repo.createConflict = domain.ErrEmailAlreadyExists
	repo.conflictBinds = true
	sessions := &credentialSessionStoreStub{}
	uc := NewLoginUseCase(repo, registrationHasherStub{}, sessions)

	result, pending, err := uc.LoginGitHub(context.Background(), githubAppProfile())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, pending)
	require.EqualValues(t, 77, result.User.ID)
	require.EqualValues(t, 77, sessions.created.UserID)
}

func TestGitHubConcurrentEmailConflictFallsBackToMailboxProof(t *testing.T) {
	configureGitHubAppTest(t)
	repo := newGitHubLoginRepoStub()
	repo.createConflict = domain.ErrEmailAlreadyExists
	sessions := &credentialSessionStoreStub{}
	uc := NewLoginUseCase(repo, registrationHasherStub{}, sessions)

	result, pending, err := uc.LoginGitHub(context.Background(), githubAppProfile())

	require.NoError(t, err)
	require.Nil(t, result)
	require.EqualValues(t, 77, pending.UserID)
	require.Equal(t, "owner@example.com", pending.Email)
	require.Nil(t, sessions.created)
}
