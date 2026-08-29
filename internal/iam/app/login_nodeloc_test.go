package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type nodeLocLoginRepoStub struct{ *linuxDOLoginRepoStub }

func (r *nodeLocLoginRepoStub) FindByThirdPartyIdentity(context.Context, string, string) (*domain.User, error) {
	return nil, nil
}

func TestNodeLocFirstLoginRequiresVerifiedAccountOwnership(t *testing.T) {
	configureLinuxDOAppTest(t)
	runtimeconfig.Set("nodeloc_minimum_trust_level", "0")
	t.Cleanup(func() { runtimeconfig.Delete("nodeloc_minimum_trust_level") })
	repo := &nodeLocLoginRepoStub{newLinuxDOLoginRepoStub()}
	sessions := &credentialSessionStoreStub{}

	result, pending, err := newLinuxDOLoginUseCase(repo, newEmailCodeStoreStub(), sessions).
		LoginNodeLoc(context.Background(), NodeLocProfile{ID: "42", Username: "node-user", Email: "member@outside.test", TrustLevel: 2})

	require.NoError(t, err)
	require.Nil(t, result)
	require.NotNil(t, pending)
	require.Equal(t, "member@outside.test", pending.SuggestedEmail)
	require.Zero(t, repo.creates)
	require.Nil(t, sessions.created)
}

func TestNodeLocRejectsTrustLevelBelowMinimum(t *testing.T) {
	runtimeconfig.Set("nodeloc_minimum_trust_level", "2")
	t.Cleanup(func() { runtimeconfig.Delete("nodeloc_minimum_trust_level") })
	uc := newLinuxDOLoginUseCase(&nodeLocLoginRepoStub{newLinuxDOLoginRepoStub()}, newEmailCodeStoreStub(), &credentialSessionStoreStub{})

	_, _, err := uc.LoginNodeLoc(context.Background(), NodeLocProfile{ID: "42", TrustLevel: 1})

	require.ErrorIs(t, err, domain.ErrNodeLocTrustLevelTooLow)
}

func TestNodeLocMapsGenericIdentityErrors(t *testing.T) {
	require.ErrorIs(t, mapNodeLocIdentityError(domain.ErrThirdPartyIdentityUnavailable), domain.ErrNodeLocAccountUnavailable)
	require.ErrorIs(t, mapNodeLocIdentityError(domain.ErrThirdPartyIdentityAlreadyBound), domain.ErrNodeLocIdentityAlreadyBound)
}
