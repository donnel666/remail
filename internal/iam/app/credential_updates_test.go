package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

type credentialRepoStub struct {
	UserRepository
	byEmail      *domain.User
	byID         *domain.User
	recorded     *domain.User
	updateCalled bool
	updateOK     bool
	expectedHash string
	newHash      string
}

func (r *credentialRepoStub) FindByEmail(context.Context, string) (*domain.User, error) {
	return r.byEmail, nil
}

func (r *credentialRepoStub) FindByID(context.Context, uint) (*domain.User, error) {
	return r.byID, nil
}

func (r *credentialRepoStub) RecordLogin(context.Context, uint, string) (*domain.User, error) {
	return r.recorded, nil
}

func (r *credentialRepoStub) UpdatePassword(_ context.Context, _ uint, expectedHash, newHash string) (bool, error) {
	r.updateCalled = true
	r.expectedHash = expectedHash
	r.newHash = newHash
	return r.updateOK, nil
}

type credentialHasherStub struct{}

func (credentialHasherStub) Hash(string) (string, error) { return "new-hash", nil }
func (credentialHasherStub) Verify(password, hash string) bool {
	return password == "correct" && hash == "old-hash"
}

type credentialSessionStoreStub struct {
	SessionStore
	created *domain.Session
	deleted bool
}

func (s *credentialSessionStoreStub) Create(_ context.Context, session *domain.Session, _ int) error {
	cp := *session
	s.created = &cp
	return nil
}

func (s *credentialSessionStoreStub) DeleteByUserID(context.Context, uint) error {
	s.deleted = true
	return nil
}

func TestLoginUsesCurrentAuthorizationStateAfterCredentialCheck(t *testing.T) {
	repo := &credentialRepoStub{
		byEmail:  &domain.User{ID: 7, Email: "user@test.com", PasswordHash: "old-hash", Enabled: true, Role: domain.RoleUser, TokenVersion: 1},
		recorded: &domain.User{ID: 7, Email: "user@test.com", PasswordHash: "old-hash", Enabled: true, Role: domain.RoleSupplier, TokenVersion: 4},
	}
	sessions := &credentialSessionStoreStub{}
	result, err := NewLoginUseCase(repo, credentialHasherStub{}, sessions, nil).
		LoginVerified(context.Background(), "user@test.com", "correct", 86400)

	require.NoError(t, err)
	require.Equal(t, domain.RoleSupplier, result.User.Role)
	require.Equal(t, domain.RoleSupplier, sessions.created.Role)
	require.Equal(t, 4, sessions.created.TokenVersion)
}

func TestLoginDoesNotCreateSessionWhenCredentialSnapshotBecameStale(t *testing.T) {
	repo := &credentialRepoStub{
		byEmail: &domain.User{ID: 7, Email: "user@test.com", PasswordHash: "old-hash", Enabled: true},
	}
	sessions := &credentialSessionStoreStub{}
	_, err := NewLoginUseCase(repo, credentialHasherStub{}, sessions, nil).
		LoginVerified(context.Background(), "user@test.com", "correct", 86400)

	require.ErrorIs(t, err, domain.ErrAccountOrPasswordIncorrect)
	require.Nil(t, sessions.created)
}

func TestChangePasswordDoesNotReportSuccessWhenGuardedUpdateLosesRace(t *testing.T) {
	repo := &credentialRepoStub{
		byID:     &domain.User{ID: 7, PasswordHash: "old-hash", Enabled: true},
		updateOK: false,
	}
	sessions := &credentialSessionStoreStub{}
	err := NewChangePasswordUseCase(repo, credentialHasherStub{}, sessions).
		Change(context.Background(), 7, "correct", "new-password")

	require.ErrorIs(t, err, domain.ErrInvalidPassword)
	require.True(t, repo.updateCalled)
	require.Equal(t, "old-hash", repo.expectedHash)
	require.Equal(t, "new-hash", repo.newHash)
	require.False(t, sessions.deleted)
}
