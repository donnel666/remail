package app

import (
	"context"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

type twoFactorServiceStub struct {
	TwoFactorService
	enabled  bool
	pending  domain.LoginChallenge
	consumed bool
}

func (s *twoFactorServiceStub) Enabled(context.Context, uint) (bool, error) { return s.enabled, nil }
func (s *twoFactorServiceStub) BeginLogin(_ context.Context, _ string, pending domain.LoginChallenge, _ time.Duration) error {
	s.pending = pending
	return nil
}
func (s *twoFactorServiceStub) ConsumeLogin(context.Context, string, string) (*domain.LoginChallenge, error) {
	if s.consumed {
		return nil, domain.ErrTwoFactorCodeIncorrect
	}
	s.consumed = true
	return &s.pending, nil
}

func TestLoginTwoFactorNeverCreatesSessionBeforeCode(t *testing.T) {
	user := &domain.User{ID: 7, Email: "user@example.test", PasswordHash: "old-hash", Status: domain.UserStatusActive, TokenVersion: 3}
	repo := &credentialRepoStub{byEmail: user, byID: user, recorded: user}
	sessions := &credentialSessionStoreStub{}
	service := &twoFactorServiceStub{enabled: true}
	uc := NewLoginUseCase(repo, credentialHasherStub{}, sessions)
	uc.SetTwoFactorService(service)
	result, err := uc.Login(context.Background(), user.Email, "correct")
	require.NoError(t, err)
	require.True(t, result.TwoFactorRequired)
	require.NotEmpty(t, result.Challenge)
	require.Nil(t, result.Session)
	require.Nil(t, sessions.created)
	result, err = uc.CompleteTwoFactor(context.Background(), result.Challenge, "123456")
	require.NoError(t, err)
	require.NotNil(t, result.Session)
	require.NotNil(t, sessions.created)
}

func TestLoginTwoFactorRejectsRevokedCredentialSnapshot(t *testing.T) {
	user := &domain.User{ID: 7, Status: domain.UserStatusActive, TokenVersion: 4}
	repo := &credentialRepoStub{byID: user}
	sessions := &credentialSessionStoreStub{}
	service := &twoFactorServiceStub{pending: domain.LoginChallenge{UserID: 7, TokenVersion: 3}}
	uc := NewLoginUseCase(repo, credentialHasherStub{}, sessions)
	uc.SetTwoFactorService(service)
	_, err := uc.CompleteTwoFactor(context.Background(), "challenge", "123456")
	require.ErrorIs(t, err, domain.ErrTwoFactorCodeIncorrect)
	require.Nil(t, sessions.created)
}

type twoFactorOAuthRepo struct {
	UserRepository
	user *domain.User
}

func (r twoFactorOAuthRepo) FindByLinuxDOID(context.Context, string) (*domain.User, error) {
	return r.user, nil
}
func (r twoFactorOAuthRepo) FindByThirdPartyIdentity(context.Context, string, string) (*domain.User, error) {
	return r.user, nil
}
func (r twoFactorOAuthRepo) RecordLinuxDOLogin(context.Context, uint, string) (*domain.User, error) {
	return r.user, nil
}
func (r twoFactorOAuthRepo) RecordThirdPartyLogin(context.Context, uint, string, string) (*domain.User, error) {
	return r.user, nil
}

func TestBoundOAuthAccountsRequireTwoFactorBeforeSession(t *testing.T) {
	configureGitHubAppTest(t)
	configureLinuxDOAppTest(t)
	cases := map[string]func(*LoginUseCase) (*LoginResult, error){
		"linuxdo": func(uc *LoginUseCase) (*LoginResult, error) {
			result, _, err := uc.LoginLinuxDO(context.Background(), LinuxDOProfile{ID: "42", Active: true, TrustLevel: 100})
			return result, err
		},
		"github": func(uc *LoginUseCase) (*LoginResult, error) {
			result, _, err := uc.LoginGitHub(context.Background(), githubAppProfile())
			return result, err
		},
		"nodeloc": func(uc *LoginUseCase) (*LoginResult, error) {
			result, _, err := uc.LoginNodeLoc(context.Background(), NodeLocProfile{ID: "42", TrustLevel: 100})
			return result, err
		},
	}
	for provider, login := range cases {
		t.Run(provider, func(t *testing.T) {
			user := &domain.User{ID: 7, Email: "owner@example.com", Status: domain.UserStatusActive, TokenVersion: 3}
			sessions := &credentialSessionStoreStub{}
			service := &twoFactorServiceStub{enabled: true}
			uc := NewLoginUseCase(twoFactorOAuthRepo{user: user}, credentialHasherStub{}, sessions)
			uc.SetTwoFactorService(service)
			result, err := login(uc)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.TwoFactorRequired)
			require.NotEmpty(t, result.Challenge)
			require.Nil(t, sessions.created)
			require.EqualValues(t, 7, service.pending.UserID)
		})
	}
}
