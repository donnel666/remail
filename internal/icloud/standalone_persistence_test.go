package icloud

import (
	"context"
	"testing"
	"time"

	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCommitStandaloneValidatedAccountPersistsResourceCredentialsAndChannels(t *testing.T) {
	db, err := gormOpenStandaloneTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	if err := db.Create(&iCloudAdminTestUser{ID: 1, Status: "active", Role: "supplier"}).Error; err != nil {
		t.Fatal(err)
	}
	primaryRoot := iCloudRootModel{Type: "icloud", OwnerUserID: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&primaryRoot).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iCloudResourceModel{
		ID: primaryRoot.ID, ResourceType: "icloud", PrimaryEmail: "primary@example.com", AccountRole: "primary",
		FamilyInviteURL: "invite-token", Status: iCloudResourceNormal, ExpireAt: now.Add(30 * 24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	operatorID := uint(1)
	verifiedAt := now.Add(-time.Minute)
	preparation := iCloudImportPreparationModel{
		OperatorUserID: &operatorID, DomainResourceID: 1, ForwardToEmail: "relay@example.com",
		VerificationCode: "123456", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(-time.Second),
		CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now,
	}
	if err := db.Create(&preparation).Error; err != nil {
		t.Fatal(err)
	}
	phoneID := uint(7)
	result, err := service.CommitStandaloneValidatedAccount(context.Background(), 1, StandaloneValidatedAccount{
		Email: "child@example.com", Region: "美国区", CountryCode: "US", AccountRole: "child", ICloudOpened: true,
		FamilyInviteURL: "https://setup.icloud.com/family/messages?inviteCode=invite-token",
		PhoneNumber:     "15488768536", PhoneCountryCode: "US", PhoneSource: "matched", KitesimPhoneID: &phoneID,
		ForwardToEmail: "relay@example.com", ForwardPreparationID: preparation.ID, ExpireAt: now.Add(30 * 24 * time.Hour),
		Secret:     AppleOnboardingSecret{Password: "secret", Birthday: "2000-11-02"},
		OldChannel: &AppleOnboardingChannel{Kind: iCloudChannelWeb, Host: "p186-maildomainws.icloud.com", Cookie: "old-cookie"},
		NewChannel: &AppleOnboardingChannel{Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "new-cookie"},
	}, "icloudvalidate:test")
	if err != nil {
		t.Fatal(err)
	}
	if result.ResourceID == 0 || !result.Created || result.ValidationGeneration != 1 || result.CredentialRevision != 1 {
		t.Fatalf("unexpected commit result: %+v", result)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource, result.ResourceID).Error; err != nil {
		t.Fatal(err)
	}
	if resource.AccountRole != "child" || resource.FamilyPrimaryResourceID != nil ||
		resource.FamilyInviteURL != "https://setup.icloud.com/family/messages?inviteCode=invite-token" ||
		resource.Status != iCloudResourcePending || resource.ForSale || resource.BoundPhoneNumber != "15488768536" ||
		resource.BoundPhoneSource != "manual" || resource.SelectedForwardTo != "relay@example.com" {
		t.Fatalf("resource was not persisted: %+v", resource)
	}
	var credential iCloudResourceCredentialModel
	if err := db.First(&credential, result.ResourceID).Error; err != nil || credential.ApplePassword != "secret" {
		t.Fatalf("credential was not persisted: %v %+v", err, credential)
	}
	var channels []iCloudResourceChannelModel
	if err := db.Where("resource_id = ?", result.ResourceID).Find(&channels).Error; err != nil || len(channels) != 2 {
		t.Fatalf("channels = %d err=%v", len(channels), err)
	}
	var runs int64
	if err := db.Model(&iCloudMaintenanceRunModel{}).Where("resource_id = ?", result.ResourceID).Count(&runs).Error; err != nil || runs != 1 {
		t.Fatalf("validation runs = %d err=%v", runs, err)
	}
	if err := db.First(&preparation, preparation.ID).Error; err != nil || preparation.ConsumedAt == nil {
		t.Fatalf("forwarding preparation was not consumed: %+v err=%v", preparation, err)
	}
	if err := db.Model(&iCloudResourceModel{}).Where("id = ?", result.ResourceID).Updates(map[string]any{"status": iCloudResourceNormal, "for_sale": true}).Error; err != nil {
		t.Fatal(err)
	}
	account := StandaloneValidatedAccount{
		Email: "child@example.com", Region: "美国区", CountryCode: "US", AccountRole: "child", ICloudOpened: true,
		FamilyInviteURL: "https://setup.icloud.com/family/messages?inviteCode=replacement-token",
		PhoneNumber:     "15488768536", PhoneCountryCode: "US", PhoneSource: "matched", KitesimPhoneID: &phoneID,
		ForwardToEmail: "relay@example.com", ForwardPreparationID: preparation.ID, ExpireAt: now.Add(30 * 24 * time.Hour),
		Secret:     AppleOnboardingSecret{Password: "secret-2", Birthday: "2000-11-02"},
		OldChannel: &AppleOnboardingChannel{Kind: iCloudChannelWeb, Host: "p186-maildomainws.icloud.com", Cookie: "old-cookie-2"},
		NewChannel: &AppleOnboardingChannel{Kind: iCloudChannelAppleAccount, Host: "appleid.apple.com", Cookie: "new-cookie-2"},
	}
	if _, err := service.CommitStandaloneValidatedAccount(context.Background(), 1, account, "icloudvalidate:test-2"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&resource, result.ResourceID).Error; err != nil || !resource.ForSale || resource.Status != iCloudResourceNormal ||
		resource.FamilyPrimaryResourceID != nil ||
		resource.FamilyInviteURL != account.FamilyInviteURL {
		t.Fatalf("existing resource sale state changed: status=%s for_sale=%t err=%v", resource.Status, resource.ForSale, err)
	}
}

func gormOpenStandaloneTestDB(_ *testing.T) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&iCloudAdminTestUser{}, &iCloudRootModel{}, &iCloudResourceModel{}, &iCloudResourceCredentialModel{}, &iCloudResourceChannelModel{}, &iCloudMaintenanceRunModel{}, &iCloudAdminCommandAllocation{}, &iCloudImportPreparationModel{}, &governanceinfra.OperationLogModel{}); err != nil {
		return nil, err
	}
	return db, nil
}
