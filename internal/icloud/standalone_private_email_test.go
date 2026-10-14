package icloud

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newPrivateEmailService(t *testing.T) *Service {
	t.Helper()
	db := newAdminICloudCommandTestDB(t, t.Name())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("ALTER TABLE icloud_allocations ADD COLUMN alias_id INTEGER").Error)
	s := NewService(db, nil, nil)
	now := time.Now().UTC()
	createAdminICloudCommandResource(t, db, now, iCloudResourceModel{
		ID: 1, PrimaryEmail: "owner@example.com", Status: iCloudResourceDisabled, AliasCount: 2,
		CredentialRevision: 1, ValidationGeneration: 1,
	})
	require.NoError(t, db.Create(&[]iCloudAliasModel{
		{ID: 10, ResourceID: 1, AnonymousID: "old-id", Email: "old@icloud.com", Status: iCloudResourceNormal, ForwardToEmail: "relay@example.com"},
		{ID: 11, ResourceID: 1, AnonymousID: "other-id", Email: "other@icloud.com", Status: iCloudResourceNormal, ForwardToEmail: "relay@example.com"},
	}).Error)
	return s
}

func TestStandalonePrivateEmailReplacementPreflight(t *testing.T) {
	s := newPrivateEmailService(t)
	ctx := context.Background()
	require.ErrorIs(t, s.PrepareStandalonePrivateEmailReplacement(ctx, 9, "owner@example.com", "old@icloud.com"), ErrICloudResourceOwner)
	require.NoError(t, s.db.Model(&iCloudResourceModel{}).Where("id=1").Update("status", iCloudResourceNormal).Error)
	require.ErrorContains(t, s.PrepareStandalonePrivateEmailReplacement(ctx, 7, "owner@example.com", "old@icloud.com"), "disable the imported resource")
	require.NoError(t, s.db.Model(&iCloudResourceModel{}).Where("id=1").Updates(map[string]any{"status": iCloudResourceDisabled, "onboarding_status": iCloudOnboardingProcessing}).Error)
	require.ErrorContains(t, s.PrepareStandalonePrivateEmailReplacement(ctx, 7, "owner@example.com", "old@icloud.com"), "stop its active workflow")
	require.NoError(t, s.db.Model(&iCloudResourceModel{}).Where("id=1").Update("onboarding_status", "").Error)
	require.NoError(t, s.db.Exec("INSERT INTO icloud_allocations (id, resource_id, alias_id, status) VALUES (1, 1, 10, 'allocated')").Error)
	require.ErrorIs(t, s.PrepareStandalonePrivateEmailReplacement(ctx, 7, "owner@example.com", "old@icloud.com"), ErrICloudResourceAllocation)
	var alias iCloudAliasModel
	require.NoError(t, s.db.First(&alias, 10).Error)
	require.Equal(t, iCloudResourceNormal, alias.Status, "a failed preflight must not change the alias")
	require.NoError(t, s.db.Exec("UPDATE icloud_allocations SET status='released' WHERE id=1").Error)
	require.NoError(t, s.PrepareStandalonePrivateEmailReplacement(ctx, 7, "owner@example.com", "old@icloud.com"))
	require.NoError(t, s.db.First(&alias, 10).Error)
	require.Equal(t, iCloudResourceDisabled, alias.Status)
	require.NoError(t, s.PrepareStandalonePrivateEmailReplacement(ctx, 7, "standalone@example.com", "old@icloud.com"))
}

func TestStandalonePrivateEmailSyncKeepsResourceDisabledAndSavesCookie(t *testing.T) {
	s := newPrivateEmailService(t)
	setICloudForwardingSuffixes(t, "example.com")
	ctx := context.Background()
	active := true
	aliases := []ApplePrivateEmail{
		{ID: "other-id", EmailAddress: "other@icloud.com", Active: &active, ForwardToEmail: "relay@example.com"},
		{ID: "new-id", EmailAddress: "new@icloud.com", Active: &active, ForwardToEmail: "relay@example.com"},
	}
	channel := &AppleOnboardingChannel{Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "myacinfo=new-session", APIKey: "api-key"}
	require.NoError(t, s.db.Create(&iCloudResourceChannelModel{ResourceID: 1, Kind: iCloudChannelWeb, Cookie: "old-web-session"}).Error)
	require.NoError(t, s.PrepareStandalonePrivateEmailReplacement(ctx, 7, "owner@example.com", "old@icloud.com"))
	require.NoError(t, s.SyncStandalonePrivateEmails(ctx, 7, "owner@example.com", "old@icloud.com", aliases, channel))
	var saved []iCloudAliasModel
	require.NoError(t, s.db.Order("id").Find(&saved).Error)
	require.Len(t, saved, 3)
	require.Equal(t, iCloudResourceDeleted, saved[0].Status)
	require.Equal(t, "other@icloud.com", saved[1].Email)
	require.Equal(t, iCloudResourceNormal, saved[1].Status)
	require.Equal(t, "new@icloud.com", saved[2].Email)
	require.Equal(t, iCloudResourceNormal, saved[2].Status)
	var resource iCloudResourceModel
	require.NoError(t, s.db.First(&resource, 1).Error)
	require.Equal(t, uint(2), resource.AliasCount)
	require.Equal(t, iCloudResourceDisabled, resource.Status)
	require.Nil(t, resource.NextProvisionAt)
	require.Nil(t, resource.NextValidationAt)
	require.Equal(t, uint64(2), resource.CredentialRevision)
	var channels []iCloudResourceChannelModel
	require.NoError(t, s.db.Order("id").Find(&channels).Error)
	require.Len(t, channels, 2)
	require.Equal(t, "old-web-session", channels[0].Cookie)
	require.Equal(t, channel.Cookie, channels[1].Cookie)

	aliases = append(aliases, aliases[0])
	require.Error(t, s.SyncStandalonePrivateEmails(ctx, 7, "owner@example.com", "old@icloud.com", aliases, channel))
	require.NoError(t, s.db.First(&resource, 1).Error)
	require.Equal(t, uint64(2), resource.CredentialRevision, "an invalid snapshot must roll back all writes")
	aliases = aliases[:2]
	aliases[0].ForwardToEmail = ""
	require.ErrorIs(t, s.SyncStandalonePrivateEmails(ctx, 7, "owner@example.com", "old@icloud.com", aliases, channel), ErrICloudImportInvalid)
}

func TestStandalonePrivateEmailReplacementLock(t *testing.T) {
	r := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewService(nil, nil, nil, rdb)
	ctx := context.Background()
	release, err := s.AcquireStandalonePrivateEmailReplacement(ctx, "owner@example.com")
	require.NoError(t, err)
	_, err = s.AcquireStandalonePrivateEmailReplacement(ctx, "OWNER@EXAMPLE.COM")
	require.ErrorContains(t, err, "another private email replacement")
	r.FastForward(16 * time.Minute)
	nextRelease, err := s.AcquireStandalonePrivateEmailReplacement(ctx, "owner@example.com")
	require.NoError(t, err)
	release()
	_, err = s.AcquireStandalonePrivateEmailReplacement(ctx, "owner@example.com")
	require.Error(t, err, "the expired lock owner must not release a newer lock")
	nextRelease()
	finalRelease, err := s.AcquireStandalonePrivateEmailReplacement(ctx, "owner@example.com")
	require.NoError(t, err)
	finalRelease()
}
