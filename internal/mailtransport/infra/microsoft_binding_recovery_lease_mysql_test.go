package infra

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMicrosoftBindingRecoveryLeaseSerializesOnlySameMaskMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	for _, resourceID := range []uint{1050, 1051, 1052} {
		createMicrosoftAliasTestResource(t, db, resourceID, "normal")
	}
	store := NewMicrosoftBindingRecoveryLeaseStore(db)
	ctx := context.Background()
	leaseUntil := time.Now().UTC().Add(time.Minute)

	tokenA, claimed, err := store.Claim(ctx, "A*****B@Recovery.Test", 1050, leaseUntil)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEmpty(t, tokenA)

	duplicateToken, claimed, err := store.Claim(ctx, "a*****b@recovery.test", 1051, leaseUntil)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Empty(t, duplicateToken)
	require.NoError(t, db.Create(&MicrosoftBindingRecoveryLeaseModel{
		NormalizedMask: "z*****0@recovery.test",
		ClaimToken:     "expired-lease-token-00000000000",
		LeaseUntil:     time.Now().UTC().Add(-time.Minute),
		ResourceID:     1052,
	}).Error)

	tokenB, claimed, err := store.Claim(ctx, "x*****9@recovery.test", 1051, leaseUntil)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEmpty(t, tokenB)
	var expiredCount int64
	require.NoError(t, db.Model(&MicrosoftBindingRecoveryLeaseModel{}).
		Where("normalized_mask = ?", "z*****0@recovery.test").Count(&expiredCount).Error)
	require.Zero(t, expiredCount)

	require.NoError(t, store.MarkSent(ctx, "a*****b@recovery.test", tokenA, time.Now().UTC()))
	require.Error(t, store.MarkSent(ctx, "a*****b@recovery.test", "wrong-token", time.Now().UTC()))
	var lease MicrosoftBindingRecoveryLeaseModel
	require.NoError(t, db.Where("normalized_mask = ?", "a*****b@recovery.test").Take(&lease).Error)
	require.NotNil(t, lease.SentAt)

	require.NoError(t, store.Release(ctx, "a*****b@recovery.test", tokenA))
	_, claimed, err = store.Claim(ctx, "a*****b@recovery.test", 1052, leaseUntil)
	require.NoError(t, err)
	require.True(t, claimed)
}
