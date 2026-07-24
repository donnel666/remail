package msacl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
)

type recoveryLeaseStoreStub struct {
	claims               []string
	sent                 []string
	released             []string
	releaseContextActive bool
	markErr              error
	expiresAt            time.Time
}

func (s *recoveryLeaseStoreStub) Claim(_ context.Context, mask string, _ uint, expiresAt time.Time) (string, bool, error) {
	s.claims = append(s.claims, mask)
	s.expiresAt = expiresAt
	return mask + "-token", true, nil
}

func TestRecoveryLeaseCoversConfiguredCodeWait(t *testing.T) {
	runtimeconfig.Set("recovery_code_lease_minutes", "1")
	runtimeconfig.Set("password_recovery_code_wait_seconds", "90")
	t.Cleanup(func() {
		runtimeconfig.Delete("recovery_code_lease_minutes")
		runtimeconfig.Delete("password_recovery_code_wait_seconds")
	})
	store := &recoveryLeaseStoreStub{}
	SetRecoveryLeaseStore(store)
	t.Cleanup(func() { SetRecoveryLeaseStore(nil) })
	startedAt := time.Now().UTC()

	_, err := claimCodeMailLease(WithRecoveryLeaseScope(context.Background(), 42, "a*****b@recovery.test"), "a*****b@recovery.test")

	require.NoError(t, err)
	require.False(t, store.expiresAt.Before(startedAt.Add(120*time.Second)))
}

func (s *recoveryLeaseStoreStub) MarkSent(_ context.Context, mask, _ string, _ time.Time) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.sent = append(s.sent, mask)
	return nil
}

func (s *recoveryLeaseStoreStub) Release(ctx context.Context, mask, _ string) error {
	s.releaseContextActive = ctx.Err() == nil
	s.released = append(s.released, mask)
	return nil
}

func TestCodeMailLeaseReusesSameMaskAndSeparatesDifferentMasks(t *testing.T) {
	store := &recoveryLeaseStoreStub{}
	SetRecoveryLeaseStore(store)
	defer SetRecoveryLeaseStore(nil)
	ctx := WithRecoveryLeaseScope(context.Background(), 42, "a*****b@recovery.test")

	first, err := claimCodeMailLease(ctx, "A*****B@Recovery.Test")
	require.NoError(t, err)
	again, err := claimCodeMailLease(ctx, "a*****b@recovery.test")
	require.NoError(t, err)
	require.Same(t, first, again)

	other, err := claimCodeMailLease(ctx, "x*****9@recovery.test")
	require.NoError(t, err)
	require.Equal(t, []string{"a*****b@recovery.test", "x*****9@recovery.test"}, store.claims)

	require.NoError(t, first.markSent(ctx))
	require.Error(t, first.markSent(ctx))
	_, err = claimCodeMailLease(ctx, "a*****b@recovery.test")
	require.Error(t, err)
	first.releaseIfUnsent(ctx)
	other.releaseIfUnsent(ctx)
	require.Equal(t, []string{"a*****b@recovery.test"}, store.sent)
	require.Equal(t, []string{"x*****9@recovery.test"}, store.released)

	replacement, err := claimCodeMailLease(ctx, "x*****9@recovery.test")
	require.NoError(t, err)
	require.NotSame(t, other, replacement)
	require.Equal(t, []string{"a*****b@recovery.test", "x*****9@recovery.test", "x*****9@recovery.test"}, store.claims)

	releaser := RecoveryLeaseReleaser(ctx)
	require.NotNil(t, releaser)
	require.NoError(t, releaser(ctx))
	require.ElementsMatch(t, []string{"x*****9@recovery.test", "a*****b@recovery.test", "x*****9@recovery.test"}, store.released)
}

func TestNormalizeRecoveryMaskRejectsMultipleAtSigns(t *testing.T) {
	require.Empty(t, normalizeRecoveryMask("a*****b@recovery.test@other.test"))
}

func TestNestedRecoveryLeaseScopeReusesSameMaskLease(t *testing.T) {
	store := &recoveryLeaseStoreStub{}
	SetRecoveryLeaseStore(store)
	defer SetRecoveryLeaseStore(nil)

	outer := WithRecoveryLeaseScope(context.Background(), 42, "")
	nested := WithRecoveryLeaseScope(outer, 42, "a*****b@recovery.test")
	first, err := claimCodeMailLease(nested, "a*****b@recovery.test")
	require.NoError(t, err)
	require.NoError(t, first.markSent(nested))

	again := WithRecoveryLeaseScope(nested, 42, "a*****b@recovery.test")
	_, err = claimCodeMailLease(again, "a*****b@recovery.test")
	require.Error(t, err)
	require.Equal(t, []string{"a*****b@recovery.test"}, store.claims)
}

func TestCodeMailLeaseMarkFailureDoesNotBecomeSent(t *testing.T) {
	store := &recoveryLeaseStoreStub{markErr: errors.New("lease lost")}
	SetRecoveryLeaseStore(store)
	defer SetRecoveryLeaseStore(nil)
	ctx := WithRecoveryLeaseScope(context.Background(), 42, "a*****b@recovery.test")

	lease, err := claimCodeMailLease(ctx, "a*****b@recovery.test")
	require.NoError(t, err)
	require.Error(t, lease.markSent(ctx))
	require.False(t, lease.sent)
	lease.releaseIfUnsent(ctx)
	require.Equal(t, []string{"a*****b@recovery.test"}, store.released)
}

func TestCompletedCodeMailLeaseReleasesAfterCallerCancellation(t *testing.T) {
	store := &recoveryLeaseStoreStub{}
	SetRecoveryLeaseStore(store)
	defer SetRecoveryLeaseStore(nil)
	ctx, cancel := context.WithCancel(WithRecoveryLeaseScope(context.Background(), 42, "a*****b@recovery.test"))
	lease, err := claimCodeMailLease(ctx, "a*****b@recovery.test")
	require.NoError(t, err)
	require.NoError(t, lease.markSent(ctx))
	cancel()

	releaseCompletedCodeMailLease(ctx, lease)

	require.Equal(t, []string{"a*****b@recovery.test"}, store.released)
	require.True(t, store.releaseContextActive)
	require.Nil(t, RecoveryLeaseReleaser(ctx), "completed mail must leave no live lease in the protocol scope")
}
