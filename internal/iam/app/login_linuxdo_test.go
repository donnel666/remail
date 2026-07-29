package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type linuxDOLoginRepoStub struct {
	UserRepository
	user      *domain.User
	linuxDOID string
	creates   int
	conflict  bool
}

func (r *linuxDOLoginRepoStub) FindByLinuxDOID(_ context.Context, linuxDOID string) (*domain.User, error) {
	if r.user == nil || r.linuxDOID != linuxDOID {
		return nil, nil
	}
	cp := *r.user
	return &cp, nil
}

func (r *linuxDOLoginRepoStub) CreateWithLinuxDOIdentity(_ context.Context, user *domain.User, linuxDOID string) error {
	r.creates++
	if r.conflict {
		r.user = &domain.User{ID: 99, Email: "linuxdo-42@oauth.invalid", Status: domain.UserStatusActive, Role: domain.RoleUser}
		r.linuxDOID = linuxDOID
		return domain.ErrEmailAlreadyExists
	}
	user.ID = 42
	cp := *user
	r.user = &cp
	r.linuxDOID = linuxDOID
	return nil
}

func (r *linuxDOLoginRepoStub) RecordLinuxDOLogin(_ context.Context, userID uint, linuxDOID string) (*domain.User, error) {
	if r.user == nil || r.user.ID != userID || r.linuxDOID != linuxDOID {
		return nil, nil
	}
	cp := *r.user
	return &cp, nil
}

func TestLinuxDORegistrationRewardIsGrantedOnlyForCreatedUser(t *testing.T) {
	runtimeconfig.Set("register_enabled", "true")
	runtimeconfig.Set("linuxdo_minimum_trust_level", "0")
	runtimeconfig.Set("registration_reward_amount", "5")
	t.Cleanup(func() {
		runtimeconfig.Delete("register_enabled")
		runtimeconfig.Delete("linuxdo_minimum_trust_level")
		runtimeconfig.Delete("registration_reward_amount")
	})

	repo := &linuxDOLoginRepoStub{}
	wallet := &registrationRewardWalletStub{}
	uc := NewLoginUseCase(repo, registrationHasherStub{}, &credentialSessionStoreStub{})
	uc.SetRegistrationRewardWallet(wallet)
	profile := LinuxDOProfile{ID: "42", Name: "LinuxDo User", Active: true}

	_, err := uc.LoginLinuxDO(context.Background(), profile)
	require.NoError(t, err)
	require.Equal(t, 1, repo.creates)
	require.Equal(t, 1, wallet.calls)

	_, err = uc.LoginLinuxDO(context.Background(), profile)
	require.NoError(t, err)
	require.Equal(t, 1, repo.creates)
	require.Equal(t, 1, wallet.calls)
}

func TestLinuxDOConcurrentRegistrationLoserDoesNotReceiveReward(t *testing.T) {
	runtimeconfig.Set("register_enabled", "true")
	runtimeconfig.Set("linuxdo_minimum_trust_level", "0")
	runtimeconfig.Set("registration_reward_amount", "5")
	t.Cleanup(func() {
		runtimeconfig.Delete("register_enabled")
		runtimeconfig.Delete("linuxdo_minimum_trust_level")
		runtimeconfig.Delete("registration_reward_amount")
	})

	repo := &linuxDOLoginRepoStub{conflict: true}
	wallet := &registrationRewardWalletStub{}
	uc := NewLoginUseCase(repo, registrationHasherStub{}, &credentialSessionStoreStub{})
	uc.SetRegistrationRewardWallet(wallet)

	_, err := uc.LoginLinuxDO(context.Background(), LinuxDOProfile{ID: "42", Active: true})
	require.NoError(t, err)
	require.Equal(t, 1, repo.creates)
	require.Zero(t, wallet.calls)
}

func TestLinuxDOSilencedAccountIsRejected(t *testing.T) {
	_, err := normalizeLinuxDOProfile(LinuxDOProfile{ID: "42", Active: true, Silenced: true})
	require.ErrorIs(t, err, domain.ErrLinuxDOAccountUnavailable)
}

func TestLinuxDOLoginNotifiesOnlyUsersWithRealEmail(t *testing.T) {
	runtimeconfig.Set("linuxdo_minimum_trust_level", "0")
	t.Cleanup(func() { runtimeconfig.Delete("linuxdo_minimum_trust_level") })

	for _, test := range []struct {
		name     string
		email    string
		expected int
	}{
		{name: "bound local account", email: "user@example.com", expected: 1},
		{name: "oauth-only account", email: "linuxdo-42@oauth.invalid", expected: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &linuxDOLoginRepoStub{
				user:      &domain.User{ID: 42, Email: test.email, Status: domain.UserStatusActive, Role: domain.RoleUser},
				linuxDOID: "42",
			}
			delivery := &loginDeliveryStub{}
			_, err := NewLoginUseCase(repo, registrationHasherStub{}, &credentialSessionStoreStub{}, delivery).
				LoginLinuxDO(context.Background(), LinuxDOProfile{ID: "42", Active: true})

			require.NoError(t, err)
			require.Len(t, delivery.messages, test.expected)
		})
	}
}
