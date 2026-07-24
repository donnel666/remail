package infra

import (
	"context"
	"fmt"
	"sync"
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
	require.Equal(t, int64(1), expiredCount)
	_, claimed, err = store.Claim(ctx, "z*****0@recovery.test", 1052, leaseUntil)
	require.NoError(t, err)
	require.True(t, claimed)

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

func TestMicrosoftBindingRecoveryLeaseClaimsDifferentMasksConcurrentlyMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	const workers = 16
	for i := 0; i < workers; i++ {
		createMicrosoftAliasTestResource(t, db, uint(1100+i), "normal")
	}
	store := NewMicrosoftBindingRecoveryLeaseStore(db)
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimed, err := store.Claim(
				context.Background(),
				fmt.Sprintf("a*****%x@recovery.test", i),
				uint(1100+i),
				time.Now().UTC().Add(time.Minute),
			)
			if err == nil && !claimed {
				err = fmt.Errorf("distinct mask was not claimed")
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestMicrosoftBindingRecoveryLeaseDeletesExpiredRowsInBatchesMySQL(t *testing.T) {
	db := newMailTransportMySQLTestDB(t)
	for _, resourceID := range []uint{1200, 1201, 1202, 1203} {
		createMicrosoftAliasTestResource(t, db, resourceID, "normal")
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		require.NoError(t, db.Create(&MicrosoftBindingRecoveryLeaseModel{
			NormalizedMask: fmt.Sprintf("e*****%d@recovery.test", i),
			ClaimToken:     fmt.Sprintf("expired-token-%018d", i),
			LeaseUntil:     now.Add(-time.Minute),
			ResourceID:     uint(1200 + i),
		}).Error)
	}
	require.NoError(t, db.Create(&MicrosoftBindingRecoveryLeaseModel{
		NormalizedMask: "a*****e@recovery.test",
		ClaimToken:     "active-lease-token-000000000000",
		LeaseUntil:     now.Add(time.Minute),
		ResourceID:     1203,
	}).Error)
	store := NewMicrosoftBindingRecoveryLeaseStore(db)

	deleted, err := store.DeleteExpired(context.Background(), now, 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	var expired int64
	require.NoError(t, db.Model(&MicrosoftBindingRecoveryLeaseModel{}).Where("lease_until <= ?", now).Count(&expired).Error)
	require.Equal(t, int64(1), expired)
	var active int64
	require.NoError(t, db.Model(&MicrosoftBindingRecoveryLeaseModel{}).Where("normalized_mask = ?", "a*****e@recovery.test").Count(&active).Error)
	require.Equal(t, int64(1), active)
}
