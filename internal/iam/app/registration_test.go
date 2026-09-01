package app

import (
	"context"
	"errors"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type registrationRepoStub struct {
	UserRepository
	created   *domain.User
	inviterID *uint
}

func (s *registrationRepoStub) FindByEmail(context.Context, string) (*domain.User, error) {
	return nil, nil
}

func (s *registrationRepoStub) Create(_ context.Context, user *domain.User) error {
	user.ID = 42
	s.created = user
	return nil
}

func (s *registrationRepoStub) CreateWithInvite(ctx context.Context, user *domain.User, _ string) error {
	return s.Create(ctx, user)
}

func (s *registrationRepoStub) FindInviterID(context.Context, uint) (*uint, error) {
	return s.inviterID, nil
}

type registrationHasherStub struct{}

func (registrationHasherStub) Hash(password string) (string, error) { return "hashed:" + password, nil }
func (registrationHasherStub) Verify(string, string) bool           { return false }

type registrationRewardWalletStub struct {
	calls              int
	userID             uint
	amount             string
	failures           int
	invitationCalls    int
	inviterID          uint
	inviteeID          uint
	invitationAmount   string
	invitationFailures int
}

func (s *registrationRewardWalletStub) GrantRegistrationReward(_ context.Context, userID uint, amount string) error {
	s.calls++
	s.userID = userID
	s.amount = amount
	if s.calls <= s.failures {
		return errors.New("wallet unavailable")
	}
	return nil
}

func (s *registrationRewardWalletStub) GrantInvitationReward(_ context.Context, inviterID, inviteeID uint, amount string) error {
	s.invitationCalls++
	s.inviterID = inviterID
	s.inviteeID = inviteeID
	s.invitationAmount = amount
	if s.invitationCalls <= s.invitationFailures {
		return errors.New("wallet unavailable")
	}
	return nil
}

func TestRegistrationReward(t *testing.T) {
	tests := []struct {
		name       string
		amount     string
		wantAmount string
		failures   int
		wantCalls  int
	}{
		{name: "credits configured amount", amount: "12.340000", wantAmount: "12.34", wantCalls: 1},
		{name: "zero skips wallet", amount: "0", wantCalls: 0},
		{name: "transient wallet failure retries", amount: "5", wantAmount: "5.00", failures: 1, wantCalls: 2},
		{name: "persistent wallet failure does not undo registration", amount: "5", wantAmount: "5.00", failures: 2, wantCalls: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtimeconfig.Set("register_enabled", "true")
			runtimeconfig.Set("registration_email_whitelist", "qq.com")
			runtimeconfig.Set("registration_reward_amount", tt.amount)
			t.Cleanup(func() {
				runtimeconfig.Delete("register_enabled")
				runtimeconfig.Delete("registration_email_whitelist")
				runtimeconfig.Delete("registration_reward_amount")
			})

			store := newEmailCodeStoreStub()
			store.codes[emailCodeKey("user@qq.com")] = "123456"
			repo := &registrationRepoStub{}
			wallet := &registrationRewardWalletStub{failures: tt.failures}
			uc := NewRegistrationUseCase(repo, registrationHasherStub{}, store)
			uc.SetRegistrationRewardWallet(wallet)

			user, err := uc.Register(context.Background(), "user@qq.com", "Password123!", "User", "123456", "")
			require.NoError(t, err)
			require.EqualValues(t, 42, user.ID)
			require.NotNil(t, repo.created)
			require.Equal(t, tt.wantCalls, wallet.calls)
			if tt.wantCalls > 0 {
				require.EqualValues(t, 42, wallet.userID)
				require.Equal(t, tt.wantAmount, wallet.amount)
			}
		})
	}
}

func TestInvitationRegistrationReward(t *testing.T) {
	tests := []struct {
		name       string
		amount     string
		inviterID  *uint
		failures   int
		wantCalls  int
		wantAmount string
	}{
		{name: "credits both users", amount: "500", inviterID: uintPointer(7), wantCalls: 1, wantAmount: "500.00"},
		{name: "zero skips wallet", amount: "0", inviterID: uintPointer(7)},
		{name: "admin invite has no referral reward", amount: "500"},
		{name: "transient wallet failure retries", amount: "12.340000", inviterID: uintPointer(7), failures: 1, wantCalls: 2, wantAmount: "12.34"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtimeconfig.Set("register_enabled", "true")
			runtimeconfig.Set("registration_email_whitelist", "qq.com")
			runtimeconfig.Set("registration_reward_amount", "0")
			runtimeconfig.Set("invitation_reward_amount", tt.amount)
			t.Cleanup(func() {
				runtimeconfig.Delete("register_enabled")
				runtimeconfig.Delete("registration_email_whitelist")
				runtimeconfig.Delete("registration_reward_amount")
				runtimeconfig.Delete("invitation_reward_amount")
			})

			store := newEmailCodeStoreStub()
			store.codes[emailCodeKey("user@qq.com")] = "123456"
			repo := &registrationRepoStub{inviterID: tt.inviterID}
			wallet := &registrationRewardWalletStub{invitationFailures: tt.failures}
			uc := NewRegistrationUseCase(repo, registrationHasherStub{}, store)
			uc.SetRegistrationRewardWallet(wallet)

			user, err := uc.Register(context.Background(), "user@qq.com", "Password123!", "User", "123456", "REFERRAL")
			require.NoError(t, err)
			require.EqualValues(t, 42, user.ID)
			require.Equal(t, tt.wantCalls, wallet.invitationCalls)
			if tt.wantCalls > 0 {
				require.EqualValues(t, 7, wallet.inviterID)
				require.EqualValues(t, 42, wallet.inviteeID)
				require.Equal(t, tt.wantAmount, wallet.invitationAmount)
			}
		})
	}
}

func uintPointer(value uint) *uint { return &value }
